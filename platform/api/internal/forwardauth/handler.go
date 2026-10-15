// Package forwardauth implements the /forwardauth endpoint of contract block 2: Traefik calls it for every
// request to an app host and it answers with the decision table (assertion, login redirect, 401, 403, 503).
// Every error path fails closed: nothing but an explicit allow produces a 2xx for an app host.
package forwardauth

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"platform/api/internal/access"
	"platform/api/internal/assertion"
	"platform/api/internal/audit"
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

// Config wires a Handler. CacheTTL, RequestTimeout, Now and Logger are optional.
type Config struct {
	AuthHost  string // lowercase, no scheme or port
	Apps      AppDirectory
	Sessions  Sessions
	Evaluator Evaluator
	Profiles  Profiles
	Denials   Denials
	Issuer    Issuer
	Audit     audit.Recorder
	CacheTTL  time.Duration
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
}

// New validates cfg and applies defaults.
func New(cfg Config) (*Handler, error) {
	if cfg.AuthHost == "" || cfg.Apps == nil || cfg.Sessions == nil || cfg.Evaluator == nil ||
		cfg.Profiles == nil || cfg.Denials == nil || cfg.Issuer == nil || cfg.Audit == nil {
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
	h := &Handler{cfg: cfg, log: cfg.Logger}
	if h.log == nil {
		h.log = slog.Default()
	}
	h.decisions = newTTLCache[decisionKey, cachedDecision](cfg.CacheTTL, cfg.Now)
	h.hosts = newTTLCache[string, appLookup](cfg.CacheTTL, cfg.Now)
	return h, nil
}

// Invalidate drops every cached decision and host lookup (called on NOTIFY access_changed).
func (h *Handler) Invalidate() {
	h.decisions.clear()
	h.hosts.clear()
}

// outcome is what the decision log line reports.
type outcome struct {
	decision string
	reason   string
	appID    string
	sid      string
	userID   string
	jti      string
	err      error
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.RequestTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	w.Header().Set(httpx.HeaderCacheControl, httpx.CacheNoStore)
	host := access.NormalizeHost(r.Header.Get(headerForwardedHost))
	out := h.decide(w, r, host)
	h.logOutcome(host, out)
}

func (h *Handler) logOutcome(host string, o outcome) {
	attrs := []any{"decision", o.decision, "reason", o.reason, "host", host, "app_id", o.appID, "sid", o.sid, "user_id", o.userID, "jti", o.jti}
	switch {
	case o.err != nil:
		h.log.Error("forwardauth decision", append(attrs, "error", o.err.Error())...)
	case o.decision == DecisionDeny || o.decision == DecisionError:
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

	// 2. A host without deployment is refused.
	appID, found, err := h.lookupApp(ctx, host)
	if err != nil {
		return h.unavailable(w, nav, ReasonBackend, err)
	}
	if !found {
		h.respondError(w, nav, http.StatusForbidden, "Access denied", "")
		return outcome{decision: DecisionDeny, reason: access.ReasonUnknownHost}
	}

	// 3. CORS preflight is the app's business.
	if method == http.MethodOptions && r.Header.Get(httpx.HeaderOrigin) != "" && r.Header.Get(headerACRM) != "" {
		w.WriteHeader(http.StatusOK)
		return outcome{decision: DecisionBypass, reason: ReasonPreflight, appID: appID}
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
			h.recordDenial(ctx, as, host, d)
		}
		h.respondError(w, nav, http.StatusForbidden, "Access denied", "")
		return outcome{decision: DecisionDeny, reason: d.Reason, appID: d.AppID, sid: as.SessionID}
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
		o.appID, o.sid = d.AppID, as.SessionID
		return o
	}
	w.Header().Set(HeaderAssertion, issued.Token)
	w.WriteHeader(http.StatusOK)
	return outcome{decision: DecisionAllow, reason: access.ReasonGranted, appID: d.AppID, sid: as.SessionID, jti: issued.JTI}
}

func (h *Handler) lookupApp(ctx context.Context, host string) (string, bool, error) {
	v, ok, gen := h.hosts.get(host)
	if ok {
		return v.appID, v.found, nil
	}
	appID, found, err := h.cfg.Apps.AppForHost(ctx, host)
	if err != nil {
		return "", false, err
	}
	h.hosts.put(host, appLookup{appID: appID, found: found}, gen)
	return appID, found, nil
}

// authorize returns the decision for (user, host), from the cache or freshly evaluated (fresh == true).
func (h *Handler) authorize(ctx context.Context, userID, host string) (cd cachedDecision, fresh bool, err error) {
	key := decisionKey{userID: userID, host: host}
	cached, ok, gen := h.decisions.get(key)
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
	}
	h.decisions.put(key, cd, gen)
	return cd, true, nil
}

// recordDenial writes the access_denials row. A failure is logged and never changes the response.
func (h *Handler) recordDenial(ctx context.Context, as session.AppSession, host string, d access.Decision) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), DenialRecordTimeout)
	defer cancel()
	err := h.cfg.Denials.RecordDenial(ctx, access.Denial{
		UserID: as.UserID, Host: host, AppID: d.AppID, InitiativeID: d.InitiativeID, Reason: d.Reason, SessionID: as.SessionID,
	})
	if err != nil {
		h.log.Error("cannot record access denial", "error", err.Error(), "host", host, "reason", d.Reason, "sid", as.SessionID)
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
	return outcome{decision: DecisionCallback, reason: ReasonCallbackOK, appID: as.AppID, sid: as.SessionID}
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
