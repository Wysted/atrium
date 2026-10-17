// Package forwardauth implements the /forwardauth endpoint of contract block 2: Traefik calls it for every
// request to an app host and it answers with the decision table (assertion, login redirect, 401, 403, 503).
// Every error path fails closed: nothing but an explicit allow produces a 2xx for an app host.
package forwardauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"platform/api/internal/access"
	"platform/api/internal/assertion"
	"platform/api/internal/audit"
	"platform/api/internal/hostname"
	"platform/api/internal/httpx"
	"platform/api/internal/keys"
	"platform/api/internal/session"
)

// Request headers read from Traefik (contract block 2). Everything else is ignored.
const (
	headerForwardedHost   = "X-Forwarded-Host"
	headerForwardedMethod = "X-Forwarded-Method"
	headerForwardedURI    = "X-Forwarded-Uri"
	headerAccept          = "Accept"
	headerSecFetchMode    = "Sec-Fetch-Mode"
	headerACRM            = "Access-Control-Request-Method"

	// HeaderAssertion carries the signed assertion back to Traefik (authResponseHeaders).
	HeaderAssertion = "Platform-Assertion"

	secFetchModeNavigate = "navigate"
	acceptHTML           = "text/html"
)

// Reserved paths on every app host (contract block 2).
const (
	PathCallback = "/_platform/callback"
	PathLogout   = "/_platform/logout"
	queryCode    = "code"
)

const (
	// BindCookieTTL is the lifetime of the bind cookie: the whole login, Google round trip included
	// (same as oauth_flows).
	BindCookieTTL = 10 * time.Minute
	// DenialRecordTimeout bounds the access_denials insert, which must not delay or alter the response.
	DenialRecordTimeout = 2 * time.Second
	// MaxNegativeHostEntries bounds the cache of hosts without deployment.
	MaxNegativeHostEntries = 10_000
	// signedOutPath is the page of the auth host shown after logout.
	signedOutPath = "/signed-out"
	loginPath     = "/login"
)

// Decision values of the log line.
const (
	DecisionBypass          = "bypass"
	DecisionAllow           = "allow"
	DecisionDeny            = "deny"
	DecisionLoginRedirect   = "login_redirect"
	DecisionUnauthenticated = "unauthenticated"
	DecisionCallback        = "callback"
	DecisionLogout          = "logout"
	DecisionError           = "error"
)

// Reasons of the log line beyond access.Reason*.
const (
	ReasonAuthHost        = "auth_host"
	ReasonPreflight       = "cors_preflight"
	ReasonCallbackOK      = "callback_ok"
	ReasonCallbackInvalid = "callback_invalid"
	ReasonLogoutOK        = "logout_ok"
	ReasonLogoutMethod    = "logout_method"
	ReasonLogoutCSRF      = "logout_csrf"
	ReasonNoCookie        = "no_cookie"
	ReasonInvalidSession  = "invalid_session"
	ReasonBackend         = "backend_failure"
	ReasonNoSigner        = "no_signer"
	ReasonAssertion       = "assertion_failure"
)

// Caches whose lookups are reported to the Observer.
const (
	CacheDecision     = "decision"      // access decision per (user, host)
	CacheHost         = "host"          // host -> app of known deployments
	CacheNegativeHost = "negative_host" // valid hosts without deployment
)

// Observer receives the events metrics count. Every string it gets is one of this package's constants (or an
// access.Reason*), never a value taken from a request: the implementation relies on that to keep label
// cardinality bounded. Nil in Config means none.
type Observer interface {
	// Decision reports a finished decision and how long it took.
	Decision(decision, reason string, elapsed time.Duration)
	// CacheLookup reports one lookup in the named cache (Cache* constants).
	CacheLookup(cache string, hit bool)
	// AppLogout reports a session revoked through the app-host logout.
	AppLogout()
}

type noObserver struct{}

func (noObserver) Decision(string, string, time.Duration) {}
func (noObserver) CacheLookup(string, bool)               {}
func (noObserver) AppLogout()                             {}

// AppDirectory resolves a host to its app.
type AppDirectory interface {
	AppForHost(ctx context.Context, host string) (appID string, found bool, err error)
}

// Sessions is the part of session.Service the endpoint needs.
type Sessions interface {
	ConsumeLoginCode(ctx context.Context, code, host, bind string) (session.LoginGrant, error)
	CreateAppSession(ctx context.Context, sessionID, appID, host string) (string, session.AppSession, error)
	ValidateAppSession(ctx context.Context, token, host string) (session.AppSession, error)
	RevokeUserSession(ctx context.Context, userID, sessionID string) (bool, error)
}

// Evaluator decides access (access.Evaluator).
type Evaluator interface {
	Evaluate(ctx context.Context, req access.Request) (access.Decision, error)
}

// Profiles loads the informational user data for the assertion.
type Profiles interface {
	Profile(ctx context.Context, userID string) (access.Profile, error)
}

// Denials records deny decisions.
type Denials interface {
	RecordDenial(ctx context.Context, d access.Denial) error
}

// Issuer signs assertions (assertion.Issuer).
type Issuer interface {
	Issue(req assertion.Request) (assertion.Issued, error)
}

// SeenRecorder is told which deployment host just received an authorized request (lastseen.Recorder).
type SeenRecorder interface {
	Touch(host string)
}

// Config wires a Handler. Seen, Observer, CacheTTL, RequestTimeout, Now and Logger are optional.
type Config struct {
	AuthHost  string // lowercase, no scheme or port
	Apps      AppDirectory
	Sessions  Sessions
	Evaluator Evaluator
	Profiles  Profiles
	Denials   Denials
	Issuer    Issuer
	Audit     audit.Recorder
	// Tokens looks up API tokens for the token path of portal hosts (token.go).
	Tokens Tokens
	// TokenUse receives the id of every token that authorized a request, for last_used_at. Nil disables it.
	TokenUse TokenUseRecorder
	// Seen receives the host of every authorized decision (a 200 with assertion), for last_seen_at. Nil
	// disables it.
	Seen     SeenRecorder
	Observer Observer
	CacheTTL time.Duration
	// RequestTimeout bounds each request, database calls included; on expiry the decision is the explicit
	// 503. Default httpx.RequestTimeout.
	RequestTimeout time.Duration
	Now            func() time.Time
	Logger         *slog.Logger
}

type appLookup struct {
	appID string
	found bool
}

type decisionKey struct{ userID, host string }

type cachedDecision struct {
	decision access.Decision
	profile  access.Profile
}

// Handler is the /forwardauth handler. Session state is validated on every request; only the access
// decision (per user and host) and the host lookup are cached, and a database NOTIFY clears both.
type Handler struct {
	cfg       Config
	log       *slog.Logger
	decisions *ttlCache[decisionKey, cachedDecision]
	hosts     *ttlCache[string, appLookup]
	// noHosts remembers valid hosts that have no deployment, apart from hosts so that a flood of random names
	// can never take the place of a legitimate entry. It is capped small and invalidated like hosts.
	noHosts *ttlCache[string, struct{}]
	// unknown queues the unknown_host denials for the background writer (Run). Invalidate does not touch it: it
	// is rate limiting, not authorization state.
	unknown *unknownHostRecorder
}

// New validates cfg and applies defaults.
func New(cfg Config) (*Handler, error) {
	if cfg.AuthHost == "" || cfg.Apps == nil || cfg.Sessions == nil || cfg.Evaluator == nil ||
		cfg.Profiles == nil || cfg.Denials == nil || cfg.Issuer == nil || cfg.Audit == nil || cfg.Tokens == nil {
		return nil, errors.New("forwardauth: auth host and every dependency are required")
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = DefaultCacheTTL
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = httpx.RequestTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Observer == nil {
		cfg.Observer = noObserver{}
	}
	h := &Handler{cfg: cfg, log: cfg.Logger}
	if h.log == nil {
		h.log = slog.Default()
	}
	h.decisions = newTTLCache[decisionKey, cachedDecision](cfg.CacheTTL, cfg.Now)
	h.hosts = newTTLCache[string, appLookup](cfg.CacheTTL, cfg.Now)
	h.noHosts = newBoundedTTLCache[string, struct{}](cfg.CacheTTL, MaxNegativeHostEntries, cfg.Now)
	h.unknown = newUnknownHostRecorder(cfg.Now)
	return h, nil
}

// Invalidate drops every cached decision and host lookup (called on NOTIFY access_changed).
func (h *Handler) Invalidate() {
	h.decisions.clear()
	h.hosts.clear()
	h.noHosts.clear()
}

// outcome is what the decision log line reports.
type outcome struct {
	decision string
	reason   string
	appID    string
	sid      string
	userID   string
	jti      string
	tid      string // API token id, empty for sessions
	err      error
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.RequestTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	w.Header().Set(httpx.HeaderCacheControl, httpx.CacheNoStore)
	host := access.NormalizeHost(r.Header.Get(headerForwardedHost))
	start := time.Now() // real clock: cfg.Now may be a test clock
	// Counted in a defer: a panic in decide (recovered further out, answered 503) still counts as a backend error.
	out := outcome{decision: DecisionError, reason: ReasonBackend}
	defer func() { h.cfg.Observer.Decision(out.decision, out.reason, time.Since(start)) }()
	out = h.decide(w, r, host)
	h.logOutcome(host, out)
}

func (h *Handler) logOutcome(host string, o outcome) {
	attrs := []any{"decision", o.decision, "reason", o.reason, "host", host, "app_id", o.appID, "sid", o.sid, "user_id", o.userID, "jti", o.jti, "tid", o.tid}
	switch {
	case o.err != nil:
		h.log.Error("forwardauth decision", append(attrs, "error", o.err.Error())...)
	case o.decision == DecisionDeny || o.decision == DecisionError || o.decision == DecisionUnauthenticated && o.tid != "":
		h.log.Warn("forwardauth decision", attrs...)
	default:
		h.log.Info("forwardauth decision", attrs...)
	}
}

// decide runs the decision table in order and writes the response.
func (h *Handler) decide(w http.ResponseWriter, r *http.Request, host string) outcome {
	ctx := r.Context()
	method := strings.ToUpper(strings.TrimSpace(r.Header.Get(headerForwardedMethod)))
	if method == "" {
		method = r.Method
	}
	nav := isNavigation(r.Header, method)

	// 1. The auth host itself is never gated.
	if host == h.cfg.AuthHost {
		w.WriteHeader(http.StatusOK)
		return outcome{decision: DecisionBypass, reason: ReasonAuthHost}
	}

	// 2. A host that cannot be a deployment host (not a valid DNS name) is refused without touching the
	// database or any cache; so is a host without deployment.
	if !hostname.Valid(host) {
		h.unknown.invalid.Add(1)
		h.respondError(w, nav, http.StatusForbidden, "Access denied", "")
		return outcome{decision: DecisionDeny, reason: access.ReasonUnknownHost}
	}
	appID, found, err := h.lookupApp(ctx, host)
	if err != nil {
		return h.unavailable(w, nav, ReasonBackend, err)
	}
	if !found {
		h.unknown.offer(host)
		h.respondError(w, nav, http.StatusForbidden, "Access denied", "")
		return outcome{decision: DecisionDeny, reason: access.ReasonUnknownHost}
	}

	// 3. CORS preflight is the app's business.
	if method == http.MethodOptions && r.Header.Get(httpx.HeaderOrigin) != "" && r.Header.Get(headerACRM) != "" {
		w.WriteHeader(http.StatusOK)
		return outcome{decision: DecisionBypass, reason: ReasonPreflight, appID: appID}
	}

	// 3b. An Authorization header on a portal host takes the token path alone (no cookie fallback); on any other
	// host it is ignored and never read again. The preflight above wins, as the decision table says.
	if len(r.Header.Values(headerAuthorization)) > 0 {
		portal, err := h.portalHost(ctx, host)
		if err != nil {
			return h.unavailable(w, nav, ReasonBackend, err)
		}
		if portal {
			return h.tokenPath(w, r, appID, host)
		}
	}

	uri := r.Header.Get(headerForwardedURI)
	// 4. Return from the login: redeem the one-time code.
	if code, ok := callbackCode(method, uri); ok {
		return h.callback(w, r, host, appID, code, nav)
	}
	// 5. Logout.
	if pathOf(uri) == PathLogout {
		return h.logout(w, r, host, appID, method)
	}

	// 6/7. Session cookie.
	token := httpx.CookieValue(r.Header, session.CookieApp)
	if token == "" {
		return h.unauthenticated(w, r, host, appID, uri, nav, ReasonNoCookie)
	}
	as, err := h.cfg.Sessions.ValidateAppSession(ctx, token, host)
	if errors.Is(err, session.ErrInvalidSession) {
		return h.unauthenticated(w, r, host, appID, uri, nav, ReasonInvalidSession)
	}
	if err != nil {
		return h.unavailable(w, nav, ReasonBackend, err)
	}

	// 8/9. Authorization.
	cd, fresh, err := h.authorize(ctx, as.UserID, host)
	if err != nil {
		return h.unavailable(w, nav, ReasonBackend, err)
	}
	d := cd.decision
	if !d.Allow {
		if fresh {
			h.recordDenial(ctx, as.UserID, as.SessionID, host, d)
		}
		h.respondDenied(w, nav, cd.profile.Email)
		return outcome{decision: DecisionDeny, reason: d.Reason, appID: d.AppID, sid: as.SessionID, userID: as.UserID}
	}
	issued, err := h.cfg.Issuer.Issue(assertion.Request{
		Audience: d.AppID, Subject: as.UserID, Email: cd.profile.Email, Name: cd.profile.Name,
		SessionID: as.SessionID, Roles: d.Roles,
	})
	if err != nil {
		reason := ReasonAssertion
		if errors.Is(err, keys.ErrNoActiveKey) {
			reason = ReasonNoSigner
		}
		o := h.unavailable(w, nav, reason, err)
		o.appID, o.sid, o.userID = d.AppID, as.SessionID, as.UserID
		return o
	}
	w.Header().Set(HeaderAssertion, issued.Token)
	w.WriteHeader(http.StatusOK)
	if h.cfg.Seen != nil {
		h.cfg.Seen.Touch(host) // memory only; the flusher writes last_seen_at
	}
	return outcome{decision: DecisionAllow, reason: access.ReasonGranted, appID: d.AppID, sid: as.SessionID, userID: as.UserID, jti: issued.JTI}
}

func (h *Handler) lookupApp(ctx context.Context, host string) (string, bool, error) {
	v, ok, gen := h.hosts.get(host)
	h.cfg.Observer.CacheLookup(CacheHost, ok)
	if ok {
		return v.appID, v.found, nil
	}
	_, missing, ngen := h.noHosts.get(host)
	h.cfg.Observer.CacheLookup(CacheNegativeHost, missing)
	if missing {
		return "", false, nil
	}
	appID, found, err := h.cfg.Apps.AppForHost(ctx, host)
	if err != nil {
		return "", false, err
	}
	if found {
		h.hosts.put(host, appLookup{appID: appID, found: true}, gen)
	} else {
		h.noHosts.put(host, struct{}{}, ngen)
	}
	return appID, found, nil
}

// authorize returns the decision for (user, host), from the cache or freshly evaluated (fresh == true).
func (h *Handler) authorize(ctx context.Context, userID, host string) (cd cachedDecision, fresh bool, err error) {
	key := decisionKey{userID: userID, host: host}
	cached, ok, gen := h.decisions.get(key)
	h.cfg.Observer.CacheLookup(CacheDecision, ok)
	if ok {
		return cached, false, nil
	}
	d, err := h.cfg.Evaluator.Evaluate(ctx, access.Request{
		Subject:  access.Subject{ID: userID},
		Resource: access.Resource{Type: access.ResourceTypeApp, ID: host},
		Action:   access.Action{Name: access.ActionAccess},
	})
	if err != nil {
		return cachedDecision{}, false, err
	}
	cd.decision = d
	if d.Allow {
		if cd.profile, err = h.cfg.Profiles.Profile(ctx, userID); err != nil {
			return cachedDecision{}, false, err
		}
	} else if p, perr := h.cfg.Profiles.Profile(ctx, userID); perr == nil {
		// Informational only (the denied page names the account); a failure must not change the decision.
		cd.profile = p
	}
	h.decisions.put(key, cd, gen)
	return cd, true, nil
}

// recordDenial writes the access_denials row. A failure is logged and never changes the response.
// sid is empty on the token path, which has no session.
func (h *Handler) recordDenial(ctx context.Context, userID, sid, host string, d access.Decision) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), DenialRecordTimeout)
	defer cancel()
	err := h.cfg.Denials.RecordDenial(ctx, access.Denial{
		UserID: userID, Host: host, AppID: d.AppID, InitiativeID: d.InitiativeID, Reason: d.Reason, SessionID: sid,
	})
	if err != nil {
		h.log.Error("cannot record access denial", "error", err.Error(), "host", host, "reason", d.Reason, "sid", sid)
	}
}

// callback redeems /_platform/callback?code=. Any failure is a 403 and the code stays burnt.
func (h *Handler) callback(w http.ResponseWriter, r *http.Request, host, appID, code string, nav bool) outcome {
	ctx := r.Context()
	bind := httpx.CookieValue(r.Header, session.CookieBind)
	grant, err := h.cfg.Sessions.ConsumeLoginCode(ctx, code, host, bind)
	if errors.Is(err, session.ErrInvalidCode) {
		h.respondError(w, nav, http.StatusForbidden, "Login could not be completed", "The login code is invalid, expired or was already used.")
		return outcome{decision: DecisionDeny, reason: ReasonCallbackInvalid, appID: appID}
	}
	if err != nil {
		return h.unavailable(w, nav, ReasonBackend, err)
	}
	token, as, err := h.cfg.Sessions.CreateAppSession(ctx, grant.SessionID, grant.AppID, grant.Host)
	if errors.Is(err, session.ErrInvalidSession) {
		h.respondError(w, nav, http.StatusForbidden, "Login could not be completed", "The session is no longer valid.")
		return outcome{decision: DecisionDeny, reason: ReasonInvalidSession, appID: appID, sid: grant.SessionID}
	}
	if err != nil {
		return h.unavailable(w, nav, ReasonBackend, err)
	}
	httpx.SetCookie(w, session.CookieApp, token, time.Until(as.ExpiresAt))
	httpx.ClearCookie(w, session.CookieBind)
	w.Header().Set(httpx.HeaderReferrerPolicy, httpx.ReferrerNone)
	w.Header().Set(httpx.HeaderLocation, grant.RD)
	w.WriteHeader(http.StatusFound)
	return outcome{decision: DecisionCallback, reason: ReasonCallbackOK, appID: as.AppID, sid: as.SessionID, userID: as.UserID}
}

// logout handles POST /_platform/logout: it revokes the central session behind the app session (the
// cascade removes every app session), clears the app cookie and sends the browser to the signed-out page.
func (h *Handler) logout(w http.ResponseWriter, r *http.Request, host, appID, method string) outcome {
	if method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusMethodNotAllowed, Detail: "logout accepts POST only"})
		return outcome{decision: DecisionDeny, reason: ReasonLogoutMethod, appID: appID}
	}
	if !httpx.SameOrigin(r.Header, host) {
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusForbidden, Detail: "cross-origin logout refused"})
		return outcome{decision: DecisionDeny, reason: ReasonLogoutCSRF, appID: appID}
	}
	out := outcome{decision: DecisionLogout, reason: ReasonLogoutOK, appID: appID}
	if token := httpx.CookieValue(r.Header, session.CookieApp); token != "" {
		as, err := h.cfg.Sessions.ValidateAppSession(r.Context(), token, host)
		switch {
		case err == nil:
			out.sid, out.userID = as.SessionID, as.UserID
			revoked, err := h.cfg.Sessions.RevokeUserSession(r.Context(), as.UserID, as.SessionID)
			if err != nil {
				return h.unavailable(w, false, ReasonBackend, err)
			}
			if revoked {
				h.cfg.Observer.AppLogout()
				audit.Write(r.Context(), h.cfg.Audit, h.log, audit.Event{
					ActorID: as.UserID, Action: audit.ActionLogout, TargetType: audit.TargetSession, TargetID: as.SessionID,
					After: map[string]string{"via": "app", "host": host, "app_id": appID},
				})
			}
		case !errors.Is(err, session.ErrInvalidSession):
			return h.unavailable(w, false, ReasonBackend, err)
		}
	}
	httpx.ClearCookie(w, session.CookieApp)
	w.Header().Set(httpx.HeaderLocation, "https://"+h.cfg.AuthHost+signedOutPath)
	w.WriteHeader(http.StatusSeeOther)
	return out
}

// unauthenticated answers a request without a valid app session: a login redirect for navigations, 401
// (with login_url) for API calls. Both start a login, so both carry a bind cookie.
func (h *Handler) unauthenticated(w http.ResponseWriter, r *http.Request, host, appID, uri string, nav bool, reason string) outcome {
	rd := "https://" + host + "/"
	if nav {
		rd = originalURL(host, uri)
	}
	loginURL, err := h.startLogin(w, r, rd)
	if err != nil {
		return h.unavailable(w, nav, ReasonBackend, err)
	}
	if nav {
		w.Header().Set(httpx.HeaderLocation, loginURL)
		w.WriteHeader(http.StatusFound)
		return outcome{decision: DecisionLoginRedirect, reason: reason, appID: appID}
	}
	httpx.WriteProblem(w, httpx.Problem{Status: http.StatusUnauthorized, Detail: "authentication required", LoginURL: loginURL})
	return outcome{decision: DecisionUnauthenticated, reason: reason, appID: appID}
}

// startLogin sets the bind cookie and returns the auth-host login URL. A well-formed bind cookie already
// present is kept (its secret never left the browser), so parallel requests and tabs do not invalidate
// each other's login; its lifetime is renewed.
func (h *Handler) startLogin(w http.ResponseWriter, r *http.Request, rd string) (string, error) {
	value := httpx.CookieValue(r.Header, session.CookieBind)
	hash, ok := session.HashToken(value)
	if !ok {
		var err error
		if value, hash, err = session.NewBind(); err != nil {
			return "", err
		}
	}
	httpx.SetCookie(w, session.CookieBind, value, BindCookieTTL)
	q := url.Values{"rd": {rd}, "bind": {base64.RawURLEncoding.EncodeToString(hash)}}
	return "https://" + h.cfg.AuthHost + loginPath + "?" + q.Encode(), nil
}

func (h *Handler) unavailable(w http.ResponseWriter, nav bool, reason string, err error) outcome {
	h.respondError(w, nav, http.StatusServiceUnavailable, "Service unavailable", "")
	return outcome{decision: DecisionError, reason: reason, err: err}
}

// respondError writes an HTML page for navigations and problem+json for everything else.
func (h *Handler) respondError(w http.ResponseWriter, nav bool, status int, title, detail string) {
	if !nav {
		httpx.WriteProblem(w, httpx.Problem{Status: status, Title: title, Detail: detail})
		return
	}
	httpx.SetPageHeaders(w.Header())
	msg := detail
	if msg == "" {
		msg = http.StatusText(status)
	}
	httpx.WritePage(w, status, title, msg)
}

// deniedPage is the 403 shown to a signed-in user without access to the app. It reflects nothing from the
// request: the only dynamic value is the account email from the database (html/template escapes it). The
// Sign out form posts to the same host, where the logout path is served by this handler, so it works without
// JavaScript and even though the user never reaches the app.
var deniedPage = template.Must(template.New("denied").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Access denied</title></head><body>
<h1>Access denied</h1>
<p>{{if .Email}}You are signed in as {{.Email}}, which does not have access to this application.{{else}}You do not have access to this application.{{end}}</p>
<form method="post" action="` + PathLogout + `"><button type="submit">Sign out</button></form>
</body></html>`))

// respondDenied answers an authenticated user without access: the page with the Sign out button for
// navigations, the usual problem+json otherwise.
func (h *Handler) respondDenied(w http.ResponseWriter, nav bool, email string) {
	if !nav {
		h.respondError(w, nav, http.StatusForbidden, "Access denied", "")
		return
	}
	var buf bytes.Buffer
	if err := deniedPage.Execute(&buf, struct{ Email string }{email}); err != nil {
		h.respondError(w, nav, http.StatusForbidden, "Access denied", "")
		return
	}
	httpx.SetPageHeaders(w.Header())
	httpx.WriteHTML(w, http.StatusForbidden, buf.String())
}

// isNavigation reports whether the request is a browser navigation, i.e. one that can follow a login
// redirect. Only GET and HEAD qualify: a redirect drops a request body and a login round trip ends in a GET
// of the original URL, so a non-idempotent request always gets 401 instead of being replayed. When the browser
// sends Sec-Fetch-Mode it decides alone (navigate means navigation, any other value means API call, even with
// Accept: text/html); without it (older clients) the Accept header is the fallback.
func isNavigation(h http.Header, method string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if mode := h.Get(headerSecFetchMode); mode != "" {
		return mode == secFetchModeNavigate
	}
	return strings.Contains(strings.ToLower(h.Get(headerAccept)), acceptHTML)
}

// callbackCode extracts the code of a GET to the callback path.
func callbackCode(method, uri string) (string, bool) {
	if method != http.MethodGet {
		return "", false
	}
	u, err := url.ParseRequestURI(uri)
	if err != nil || u.Path != PathCallback {
		return "", false
	}
	code := u.Query().Get(queryCode)
	return code, code != ""
}

func pathOf(uri string) string {
	u, err := url.ParseRequestURI(uri)
	if err != nil {
		return ""
	}
	return u.Path
}

// originalURL rebuilds the URL the browser asked for. It must pass the same rules the auth host applies to rd
// (session.WellFormedRD: no "//" in the decoded or escaped path, no backslash, length bound...); otherwise the
// login would end in a 400, so it falls back to the app root.
func originalURL(host, uri string) string {
	rd := "https://" + host + "/"
	if !strings.HasPrefix(uri, "/") {
		return rd
	}
	if full := rd[:len(rd)-1] + uri; session.WellFormedRD(full) {
		return full
	}
	return rd
}
