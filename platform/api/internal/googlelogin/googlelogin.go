// Package googlelogin implements the OpenID Connect authorization-code flow against Google (contract block 5):
// PKCE S256, state, nonce, ID token verification and the domain checks go-oidc does not do. It owns the
// protocol and the oauth_flows rules; cookies, users, sessions and audit belong to the auth host handler.
//
// Resilience: the OIDC discovery (Provider metadata and JWKS location) runs in the background (Run) with
// exponential backoff and never blocks start-up. While it has not succeeded, Begin and Complete return
// ErrNotReady, which the auth host maps to 503 "login provider unavailable". Nothing else depends on Google:
// /readyz and /forwardauth keep serving existing sessions through an outage.
//
// Failures are split in two: a provider that answered "no" (invalid_grant, a bad ID token, a rejected identity)
// is a 400/403, while a provider that could not answer (token endpoint down or hanging, unexpected status,
// signing keys unreachable, request deadline hit) is ErrProviderUnavailable and the auth host answers 503 with
// Retry-After, like ErrNotReady.
//
// Secrets: the authorization code, the access token and the ID token live only in local variables of Complete.
// They are never stored, logged or returned; error values built from the token endpoint carry the status and
// the OAuth error code only.
package googlelogin

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"platform/api/internal/session"
)

const (
	// GoogleIssuer is the issuer of Google accounts. It is fixed in production; only tests use another one.
	GoogleIssuer = "https://accounts.google.com"

	// FlowTTL is the lifetime of an oauth_flows row and of the login cookie (contract block 5).
	FlowTTL = 10 * time.Minute

	// Scopes requested from Google.
	scopeOpenID  = oidc.ScopeOpenID
	scopeEmail   = "email"
	scopeProfile = "profile"

	// Authorization request parameters.
	paramPrompt     = "prompt"
	promptSelect    = "select_account"
	paramHostedDom  = "hd"
	idTokenKey      = "id_token"
	signingAlgRS256 = "RS256"

	// Discovery retry policy.
	DefaultInitialBackoff = time.Second
	DefaultMaxBackoff     = time.Minute
	backoffFactor         = 2
	// DefaultHTTPTimeout bounds every request to Google (discovery, token endpoint, JWKS).
	DefaultHTTPTimeout = 10 * time.Second

	// FakeSubjectPrefix namespaces the google_sub of the fake-login users. A real Google subject never carries
	// it, and checkIdentity refuses one that does, so a Google account can never collide with a fake user.
	FakeSubjectPrefix = "fake:"

	// Statuses of a token endpoint refusal that say "the provider could not serve us" and not "the code is bad":
	// anything outside 4xx (3xx, 5xx) and 429.
	statusClientErrorMin = http.StatusBadRequest
	statusClientErrorMax = http.StatusInternalServerError

	// keyFetchFailure is the text go-oidc puts in the Verify error when it could not fetch the signing keys
	// (it wraps with %v, so there is nothing to errors.As). Covered by a test against an unreachable JWKS.
	keyFetchFailure = "fetching keys"

	// Bounds on identity fields before they reach the database.
	maxSubjectLength = 255
	maxEmailLength   = 254 // same bound as the fake login
	maxNameRunes     = 200
)

var (
	// ErrNotReady means the provider discovery has not succeeded yet.
	ErrNotReady = errors.New("login provider not ready")
	// ErrInvalidFlow covers every reason a flow cannot be completed: no or malformed cookie, unknown, used,
	// expired, missing code, state mismatch.
	ErrInvalidFlow = errors.New("invalid login flow")
	// ErrExchange means the token endpoint refused the code (a 4xx answer such as invalid_grant).
	ErrExchange = errors.New("authorization code exchange failed")
	// ErrNoCode means the callback carried the right state but no authorization code. The flow is burned.
	ErrNoCode = errors.New("authorization response without code")
	// ErrProviderUnavailable means Google could not answer: the token endpoint or the signing keys were
	// unreachable, hung, or answered with something other than a refusal, or the request deadline ended while
	// waiting. It is transient and not the user's fault.
	ErrProviderUnavailable = errors.New("login provider unavailable")
	// ErrIDToken means the ID token is missing, fails go-oidc verification or its nonce does not match.
	ErrIDToken = errors.New("invalid ID token")
)

// Reasons of a RejectedError.
const (
	ReasonEmailNotVerified = "email_not_verified"
	ReasonHDMissing        = "hd_missing"
	ReasonHDNotAllowed     = "hd_not_allowed"
	ReasonIncompleteClaims = "incomplete_claims"
)

// RejectedError is a valid, signed ID token whose identity the platform refuses. HD and Email are kept for the
// log (contract, Open items) and must never be shown to the user.
type RejectedError struct {
	Reason string
	HD     string
	Email  string
}

func (e *RejectedError) Error() string { return "login rejected: " + e.Reason }

// Identity is the verified identity of a Google account. Subject is the google_sub of users.
type Identity struct {
	Subject      string
	Email        string
	Name         string
	HostedDomain string
}

// Flow is the server-side state of one login in progress (oauth_flows).
type Flow struct {
	State    string
	Nonce    string
	Verifier string // PKCE code_verifier
	RD       string // validated post-login target
	BindHash []byte
}

// NewFlow is the input of FlowStore.InsertFlow. Only the hash of the cookie value reaches the store.
type NewFlow struct {
	TokenHash []byte
	Flow
	TTL time.Duration
}

// FlowStore persists login flows.
type FlowStore interface {
	InsertFlow(ctx context.Context, f NewFlow) error
	// ConsumeFlow atomically burns the flow identified by tokenHash, but only if state is its state: unknown,
	// used, expired or a different state is ErrInvalidFlow and consumes nothing. Matching the state in the
	// same statement means a request that does not know it (a forged callback carrying a victim's cookie, an
	// older tab) cannot cancel a login in progress. The comparison is done by the store; the state is 256
	// random bits, so a timing oracle on it is not exploitable.
	ConsumeFlow(ctx context.Context, tokenHash []byte, state string) (Flow, error)
}

// Config wires a Login. Issuer defaults to GoogleIssuer; only tests set it. Backoffs and HTTPClient are optional.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// AllowedHD are the accepted hosted domains (lowercase, non-empty). The first one is sent as the hd hint.
	AllowedHD []string
	Flows     FlowStore

	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	HTTPClient     *http.Client
	Logger         *slog.Logger
}

// client is what discovery produces; it is published atomically once complete.
type client struct {
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// Login runs the flow. It is safe for concurrent use.
type Login struct {
	cfg       Config
	allowedHD map[string]struct{}
	http      *http.Client
	log       *slog.Logger
	ready     atomic.Pointer[client]
}

// New validates cfg. It does not contact the provider: see Run.
func New(cfg Config) (*Login, error) {
	if cfg.Issuer == "" {
		cfg.Issuer = GoogleIssuer
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURL == "" || cfg.Flows == nil {
		return nil, errors.New("googlelogin: client id, client secret, redirect URL and flow store are required")
	}
	if len(cfg.AllowedHD) == 0 {
		return nil, errors.New("googlelogin: at least one allowed hosted domain is required")
	}
	allowed := make(map[string]struct{}, len(cfg.AllowedHD))
	for _, hd := range cfg.AllowedHD {
		if hd == "" || hd != strings.ToLower(hd) {
			return nil, fmt.Errorf("googlelogin: allowed hosted domain %q must be non-empty and lowercase", hd)
		}
		allowed[hd] = struct{}{}
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = DefaultInitialBackoff
	}
	if cfg.MaxBackoff < cfg.InitialBackoff {
		cfg.MaxBackoff = max(DefaultMaxBackoff, cfg.InitialBackoff)
	}
	l := &Login{cfg: cfg, allowedHD: allowed, log: cfg.Logger}
	// Never follow redirects (discovery, token endpoint and JWKS answer directly): a redirect would carry the
	// client secret to wherever it points. The caller's client is copied, not modified.
	hc := http.Client{Timeout: DefaultHTTPTimeout}
	if cfg.HTTPClient != nil {
		hc = *cfg.HTTPClient
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	l.http = &hc
	if l.log == nil {
		l.log = slog.Default()
	}
	return l, nil
}

// Ready reports whether the discovery has succeeded.
func (l *Login) Ready() bool { return l.ready.Load() != nil }

// Run performs the provider discovery until it succeeds or ctx ends, retrying with exponential backoff.
// Run it from the server's background context; it returns as soon as the provider is ready. Discovery is not
// repeated afterwards: go-oidc refreshes the signing keys by itself when it meets an unknown kid.
func (l *Login) Run(ctx context.Context) {
	backoff := l.cfg.InitialBackoff
	for attempt := 1; ; attempt++ {
		err := l.discover(ctx)
		if err == nil {
			l.log.Info("login provider ready", "issuer", l.cfg.Issuer, "attempts", attempt)
			return
		}
		if ctx.Err() != nil {
			return
		}
		l.log.Warn("login provider discovery failed, will retry", "issuer", l.cfg.Issuer, "error", err.Error(), "retry_in", backoff.String())
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*backoffFactor, l.cfg.MaxBackoff)
	}
}

func (l *Login) discover(ctx context.Context) error {
	dctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, l.http), l.http.Timeout+time.Second)
	defer cancel()
	// The context only configures the requests (HTTP client); go-oidc keeps the client for later key fetches.
	provider, err := oidc.NewProvider(dctx, l.cfg.Issuer)
	if err != nil {
		return err
	}
	l.ready.Store(&client{
		oauth: &oauth2.Config{
			ClientID: l.cfg.ClientID, ClientSecret: l.cfg.ClientSecret, Endpoint: provider.Endpoint(),
			RedirectURL: l.cfg.RedirectURL, Scopes: []string{scopeOpenID, scopeEmail, scopeProfile},
		},
		// Only RS256 (what Google signs with); the issuer check stays on, never SkipIssuerCheck.
		verifier: provider.Verifier(&oidc.Config{ClientID: l.cfg.ClientID, SupportedSigningAlgs: []string{signingAlgRS256}}),
	})
	return nil
}

// BeginInput is a login request: RD is the validated post-login target and BindHash the SHA-256 of the bind cookie.
type BeginInput struct {
	RD       string
	BindHash []byte
}

// Begin stores a new flow and returns the cookie value that identifies it and the authorization URL to send
// the browser to. Only the hash of the cookie value is stored.
func (l *Login) Begin(ctx context.Context, in BeginInput) (cookieValue, authURL string, err error) {
	c := l.ready.Load()
	if c == nil {
		return "", "", ErrNotReady
	}
	cookieValue, hash, err := session.NewToken()
	if err != nil {
		return "", "", err
	}
	state, _, err := session.NewToken()
	if err != nil {
		return "", "", err
	}
	nonce, _, err := session.NewToken()
	if err != nil {
		return "", "", err
	}
	verifier := oauth2.GenerateVerifier()
	err = l.cfg.Flows.InsertFlow(ctx, NewFlow{
		TokenHash: hash, TTL: FlowTTL,
		Flow: Flow{State: state, Nonce: nonce, Verifier: verifier, RD: in.RD, BindHash: in.BindHash},
	})
	if err != nil {
		return "", "", fmt.Errorf("store login flow: %w", err)
	}
	// hd is a UI hint only; the real check is on the verified ID token.
	authURL = c.oauth.AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce), oauth2.AccessTypeOnline,
		oauth2.SetAuthURLParam(paramPrompt, promptSelect), oauth2.SetAuthURLParam(paramHostedDom, l.cfg.AllowedHD[0]))
	return cookieValue, authURL, nil
}

// CompleteInput is what the callback received: the login cookie value and the query parameters.
type CompleteInput struct {
	CookieValue string
	State       string
	Code        string
}

// Result is a completed login: who logged in and the target and bind hash that were stored with the flow
// (never taken from the callback request).
type Result struct {
	Identity Identity
	RD       string
	BindHash []byte
}

// Abandon burns the flow behind cookieValue when Google answered with ?error=, and reports whether it did. It
// burns only when state is the flow's own: without a state, or with another one, nothing is touched, so a forged
// callback cannot cancel somebody's login. It is best effort: an unknown value or a store failure is false.
func (l *Login) Abandon(ctx context.Context, cookieValue, state string) (burned bool) {
	hash, ok := session.HashToken(cookieValue)
	if !ok || state == "" {
		return false
	}
	_, err := l.cfg.Flows.ConsumeFlow(ctx, hash, state)
	return err == nil
}

// Complete finishes the flow. Order matters: the flow is burned atomically first, and only if state is its own
// (a failed attempt after that cannot be retried; a callback with a foreign state fails with ErrInvalidFlow and
// burns nothing), then the code is exchanged with the PKCE verifier, the ID token is verified and finally the
// nonce, email_verified and hd are checked. Any error other than ErrNotReady and ErrInvalidFlow means the flow
// was burned.
func (l *Login) Complete(ctx context.Context, in CompleteInput) (Result, error) {
	c := l.ready.Load()
	if c == nil {
		return Result{}, ErrNotReady
	}
	hash, ok := session.HashToken(in.CookieValue)
	if !ok || in.State == "" {
		return Result{}, ErrInvalidFlow
	}
	flow, err := l.cfg.Flows.ConsumeFlow(ctx, hash, in.State)
	if err != nil {
		return Result{}, err
	}
	// Defense in depth: the store already matched the state; do not trust a store that did not.
	if subtle.ConstantTimeCompare([]byte(in.State), []byte(flow.State)) != 1 {
		return Result{}, ErrInvalidFlow
	}
	if in.Code == "" {
		return Result{}, ErrNoCode
	}

	tok, err := c.oauth.Exchange(oidc.ClientContext(ctx, l.http), in.Code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		if providerUnavailable(ctx, err) {
			return Result{}, fmt.Errorf("%w: %s", ErrProviderUnavailable, describeExchangeError(err))
		}
		return Result{}, fmt.Errorf("%w: %s", ErrExchange, describeExchangeError(err))
	}
	raw, _ := tok.Extra(idTokenKey).(string) // the access token in tok is dropped here, unused
	if raw == "" {
		return Result{}, fmt.Errorf("%w: response without id_token", ErrIDToken)
	}
	idt, err := c.verifier.Verify(oidc.ClientContext(ctx, l.http), raw)
	if err != nil {
		if ctx.Err() != nil || strings.Contains(err.Error(), keyFetchFailure) {
			return Result{}, fmt.Errorf("%w: signing keys unavailable", ErrProviderUnavailable)
		}
		return Result{}, fmt.Errorf("%w: %s", ErrIDToken, err.Error()) // go-oidc errors never echo the token
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(flow.Nonce)) != 1 {
		return Result{}, fmt.Errorf("%w: nonce mismatch", ErrIDToken)
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		HD            string `json:"hd"`
	}
	if err := idt.Claims(&claims); err != nil {
		return Result{}, fmt.Errorf("%w: unreadable claims", ErrIDToken)
	}
	id, err := l.checkIdentity(idt.Subject, claims.Email, claims.Name, claims.HD, claims.EmailVerified)
	if err != nil {
		return Result{}, err
	}
	return Result{Identity: id, RD: flow.RD, BindHash: flow.BindHash}, nil
}

// checkIdentity applies the platform policy to the verified claims. email_verified is strictly true and hd must
// be present and allowed (personal accounts carry no hd).
func (l *Login) checkIdentity(sub, email, name, hd string, emailVerified bool) (Identity, error) {
	email, hd = strings.ToLower(strings.TrimSpace(email)), strings.ToLower(strings.TrimSpace(hd))
	reject := func(reason string) (Identity, error) {
		return Identity{}, &RejectedError{Reason: reason, HD: hd, Email: email}
	}
	switch {
	case !emailVerified:
		return reject(ReasonEmailNotVerified)
	case hd == "":
		return reject(ReasonHDMissing)
	}
	if _, ok := l.allowedHD[hd]; !ok {
		return reject(ReasonHDNotAllowed)
	}
	if sub == "" || strings.HasPrefix(sub, FakeSubjectPrefix) || len(sub) > maxSubjectLength || email == "" || len(email) > maxEmailLength || hasNUL(sub) || hasNUL(email) {
		return reject(ReasonIncompleteClaims)
	}
	return Identity{Subject: sub, Email: email, Name: cleanName(name), HostedDomain: hd}, nil
}

func hasNUL(s string) bool { return strings.ContainsRune(s, 0) }

// cleanName makes the display name storable (valid UTF-8, no NUL) and bounded.
func cleanName(s string) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(strings.TrimSpace(s), "�"), "\x00", "")
	if utf8.RuneCountInString(s) > maxNameRunes {
		s = string([]rune(s)[:maxNameRunes])
	}
	return s
}

// providerUnavailable tells a provider that could not answer from one that refused. The request context ending
// (deadline, client gone), a transport error or an unparsable answer are unavailability; so is a refusal with a
// status outside 4xx or 429, since 3xx (redirects are never followed) and 5xx are not a verdict on the code.
// Only a 4xx RetrieveError (invalid_grant, invalid_client, ...) is a rejection.
func providerUnavailable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) {
		return true
	}
	code := re.Response.StatusCode
	return code == http.StatusTooManyRequests || code < statusClientErrorMin || code >= statusClientErrorMax
}

// describeExchangeError keeps only the HTTP status and the OAuth error code of a token endpoint refusal; the
// raw response body is dropped.
func describeExchangeError(err error) string {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return fmt.Sprintf("token endpoint answered %d %s", re.Response.StatusCode, re.ErrorCode)
	}
	return "token endpoint unreachable"
}
