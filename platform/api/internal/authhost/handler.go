// Package authhost serves the browser routes of the auth host (public listener): /login, /oauth2/callback,
// /logout and the signed-out page.
//
// Login providers: Google (Config.Login) is the real one. The fake login (Config.FakeLogin, off by default)
// trusts a posted email and exists only to run the flow without Google. They never coexist: when Google is
// configured the fake login is switched off with a WARN, because a forgotten flag next to a real provider
// would let anyone who can reach the auth host log in as any user. Both producers of a session share
// establishSession, so fixation protection, audit and the login code hand-off are identical.
package authhost

import (
	"context"
	"encoding/base64"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"platform/api/internal/audit"
	"platform/api/internal/googlelogin"
	"platform/api/internal/httpx"
	"platform/api/internal/ratelimit"
	"platform/api/internal/session"
)

const (
	// Paths of the auth host.
	PathLogin     = "/login"
	PathLogout    = "/logout"
	PathCallback  = "/oauth2/callback"
	PathSignedOut = "/signed-out"

	// callbackPath is the reserved path of every app host (contract block 2).
	callbackPath = "/_platform/callback"

	queryRD    = "rd"
	queryBind  = "bind"
	queryState = "state"
	queryCode  = "code"
	queryError = "error"
	fieldMail  = "email"

	// maxEmailLength matches the users.email CHECK.
	maxEmailLength = 254
	// maxFormBytes bounds the fake login form body.
	maxFormBytes = 8 << 10
	// providerRetryAfterSeconds is the Retry-After hint while the login provider is not ready.
	providerRetryAfterSeconds = 5
)

// Sessions is the part of session.Service the auth host needs.
type Sessions interface {
	ValidateRD(ctx context.Context, raw string) (session.RD, error)
	ValidateSession(ctx context.Context, token string) (session.Session, error)
	CreateSession(ctx context.Context, in session.CreateSessionInput) (string, session.Session, error)
	RevokeSession(ctx context.Context, token string) (session.Revoked, error)
	CreateLoginCode(ctx context.Context, in session.LoginCodeInput) (string, error)
}

// Apps resolves the app behind a registered host.
type Apps interface {
	AppForHost(ctx context.Context, host string) (appID string, found bool, err error)
}

// Users provisions users just-in-time.
type Users interface {
	UpsertUser(ctx context.Context, googleSub, email, name string) (id string, active bool, err error)
}

// Login is the Google flow the auth host drives (implemented by *googlelogin.Login).
type Login interface {
	Begin(ctx context.Context, in googlelogin.BeginInput) (cookieValue, authURL string, err error)
	Complete(ctx context.Context, in googlelogin.CompleteInput) (googlelogin.Result, error)
	Abandon(ctx context.Context, cookieValue, state string) (burned bool)
}

// Limiter decides whether a client may call /login now.
type Limiter interface {
	Allow(key string) (ok bool, retryAfter time.Duration)
}

// Config wires a Handler. RequestTimeout (default httpx.RequestTimeout) and Logger are optional.
type Config struct {
	AuthHost string
	Sessions Sessions
	Apps     Apps
	Users    Users
	Limiter  Limiter
	Audit    audit.Recorder
	// Login enables Google login and disables FakeLogin. Nil means no Google.
	Login          Login
	FakeLogin      bool
	RequestTimeout time.Duration
	Logger         *slog.Logger
}

// Handler holds the auth host routes.
type Handler struct {
	cfg Config
	log *slog.Logger
}

// New validates cfg.
func New(cfg Config) (*Handler, error) {
	if cfg.AuthHost == "" || cfg.Sessions == nil || cfg.Apps == nil || cfg.Users == nil || cfg.Limiter == nil || cfg.Audit == nil {
		return nil, errors.New("authhost: auth host and every dependency are required")
	}
	h := &Handler{cfg: cfg, log: cfg.Logger}
	if h.log == nil {
		h.log = slog.Default()
	}
	if cfg.Login != nil && cfg.FakeLogin {
		h.log.Warn("PLATFORM_FAKE_LOGIN ignored: Google login is configured and the fake login is disabled")
		h.cfg.FakeLogin = false
	}
	return h, nil
}

// Register adds the routes to mux; /oauth2/callback exists only with Google login (404 otherwise).
// Every route runs under a request deadline, so a hung database ends in the explicit 503 page.
func (h *Handler) Register(mux *http.ServeMux) {
	route := func(f http.HandlerFunc) http.Handler {
		return httpx.WithDeadline(h.cfg.RequestTimeout, httpx.WithPageHeaders(f))
	}
	mux.Handle("GET "+PathLogin, route(h.loginPage))
	mux.Handle("POST "+PathLogin, route(h.fakeLogin))
	mux.Handle("POST "+PathLogout, route(h.logout))
	mux.Handle("GET "+PathSignedOut, route(h.signedOut))
	if h.cfg.Login != nil {
		mux.Handle("GET "+PathCallback, route(h.callback))
	}
}

// loginParams is a validated login request.
type loginParams struct {
	rd       session.RD
	appID    string
	bindHash []byte
}

// errBadRequest marks a parameter problem (400) as opposed to a backend failure (503).
var errBadRequest = errors.New("bad login request")

func (h *Handler) parseParams(ctx context.Context, rawRD, rawBind string) (loginParams, error) {
	rd, appID, err := h.resolveTarget(ctx, rawRD)
	if err != nil {
		return loginParams{}, err
	}
	bind, err := base64.RawURLEncoding.Strict().DecodeString(rawBind)
	if err != nil || len(bind) != session.TokenBytes {
		return loginParams{}, errBadRequest
	}
	return loginParams{rd: rd, appID: appID, bindHash: bind}, nil
}

// resolveTarget validates the redirect target and finds the app behind its host.
func (h *Handler) resolveTarget(ctx context.Context, rawRD string) (session.RD, string, error) {
	rd, err := h.cfg.Sessions.ValidateRD(ctx, rawRD)
	if errors.Is(err, session.ErrInvalidRD) {
		return session.RD{}, "", errBadRequest
	}
	if err != nil {
		return session.RD{}, "", err
	}
	appID, found, err := h.cfg.Apps.AppForHost(ctx, rd.Host)
	if err != nil {
		return session.RD{}, "", err
	}
	if !found {
		return session.RD{}, "", errBadRequest
	}
	return rd, appID, nil
}

// loginPage handles GET /login?rd=&bind=.
func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r) {
		return
	}
	ctx := r.Context()
	p, err := h.parseParams(ctx, r.URL.Query().Get(queryRD), r.URL.Query().Get(queryBind))
	if err != nil {
		h.fail(w, err)
		return
	}
	if sess, ok, err := h.currentSession(ctx, r); err != nil {
		h.fail(w, err)
		return
	} else if ok {
		switch err := h.redirectWithCode(ctx, w, sess, p); {
		case err == nil:
			h.log.Info("login code issued", "sid", sess.ID, "host", p.rd.Host, "app_id", p.appID)
			return
		case !errors.Is(err, session.ErrInvalidSession): // the session died in between: ask for login again
			h.fail(w, err)
			return
		}
	}
	if h.cfg.Login != nil {
		h.startGoogle(w, r, p)
		return
	}
	if !h.cfg.FakeLogin {
		providerMissing(w)
		return
	}
	var page strings.Builder
	if err := loginForm.Execute(&page, formData{RD: r.URL.Query().Get(queryRD), Bind: r.URL.Query().Get(queryBind)}); err != nil {
		h.log.Error("render login form", "error", err.Error())
		httpx.WritePage(w, http.StatusInternalServerError, "Error", "Internal error.")
		return
	}
	httpx.WriteHTML(w, http.StatusOK, page.String())
}

// fakeLogin handles POST /login when PLATFORM_FAKE_LOGIN is on: it trusts the posted email.
func (h *Handler) fakeLogin(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Login != nil { // Google login has no POST form
		httpx.WritePage(w, http.StatusMethodNotAllowed, "Method not allowed", "Use the sign-in link of the application.")
		return
	}
	if !h.cfg.FakeLogin {
		providerMissing(w)
		return
	}
	if !httpx.SameOrigin(r.Header, h.cfg.AuthHost) {
		httpx.WritePage(w, http.StatusForbidden, "Access denied", "Cross-origin request refused.")
		return
	}
	if !h.allow(w, r) {
		return
	}
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		httpx.WritePage(w, http.StatusBadRequest, "Invalid request", "The form is invalid.")
		return
	}
	email, ok := parseEmail(r.PostForm.Get(fieldMail))
	if !ok {
		httpx.WritePage(w, http.StatusBadRequest, "Invalid request", "The email address is invalid.")
		return
	}
	p, err := h.parseParams(ctx, r.PostForm.Get(queryRD), r.PostForm.Get(queryBind))
	if err != nil {
		h.fail(w, err)
		return
	}
	userID, active, err := h.cfg.Users.UpsertUser(ctx, googlelogin.FakeSubjectPrefix+email, email, localPart(email))
	if err != nil {
		h.fail(w, err)
		return
	}
	if !active {
		httpx.WritePage(w, http.StatusForbidden, "Access denied", "This account is disabled.")
		return
	}
	sess, err := h.establishSession(ctx, w, r, userID, map[string]string{"method": "fake", "host": p.rd.Host})
	if err != nil {
		h.fail(w, err)
		return
	}
	if err := h.redirectWithCode(ctx, w, sess, p); err != nil {
		h.fail(w, err)
		return
	}
	h.log.Warn("fake login", "user_id", userID, "sid", sess.ID, "host", p.rd.Host)
}

// establishSession turns a successful login into a central session: the previous session of the browser (if
// any) is revoked first (no fixation, no leftover app sessions), a new one is created, the login is audited and
// the cookie is set. loginAudit is the "after" payload of the session.login event; it must carry no secret.
func (h *Handler) establishSession(ctx context.Context, w http.ResponseWriter, r *http.Request, userID string, loginAudit map[string]string) (session.Session, error) {
	if old := httpx.CookieValue(r.Header, session.CookieSession); old != "" {
		prev, err := h.cfg.Sessions.RevokeSession(ctx, old)
		if err != nil {
			return session.Session{}, err
		}
		if prev.SessionID != "" {
			audit.Write(ctx, h.cfg.Audit, h.log, audit.Event{
				ActorID: prev.UserID, Action: audit.ActionRevoke, TargetType: audit.TargetSession, TargetID: prev.SessionID,
				After: map[string]string{"reason": "replaced_by_new_login"},
			})
		}
	}
	token, sess, err := h.cfg.Sessions.CreateSession(ctx, session.CreateSessionInput{
		UserID: userID, IP: httpx.ClientIP(r), UserAgent: r.UserAgent(),
	})
	if err != nil {
		return session.Session{}, err
	}
	// The session exists from here on: audit it before anything else can fail.
	audit.Write(ctx, h.cfg.Audit, h.log, audit.Event{
		ActorID: userID, Action: audit.ActionLogin, TargetType: audit.TargetSession, TargetID: sess.ID, After: loginAudit,
	})
	httpx.SetCookie(w, session.CookieSession, token, time.Until(sess.ExpiresAt))
	return sess, nil
}

// startGoogle stores a flow and sends the browser to Google. A second /login in another tab replaces the login
// cookie, so only the latest flow of a browser can complete: the callback of the older tab arrives with the new
// cookie and its own state, matches no flow, gets a 400 and burns nothing, leaving the latest flow intact.
func (h *Handler) startGoogle(w http.ResponseWriter, r *http.Request, p loginParams) {
	cookie, authURL, err := h.cfg.Login.Begin(r.Context(), googlelogin.BeginInput{RD: p.rd.URL, BindHash: p.bindHash})
	if errors.Is(err, googlelogin.ErrNotReady) {
		h.providerUnavailable(w)
		return
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	httpx.SetCookie(w, session.CookieLogin, cookie, googlelogin.FlowTTL)
	w.Header().Set(httpx.HeaderLocation, authURL)
	w.WriteHeader(http.StatusFound)
}

// callback handles GET /oauth2/callback?code&state (or ?error= from Google). Nothing of the query is reflected
// and the redirect target comes from the stored flow, never from the request.
//
// The flow is matched by cookie AND state. A request whose state does not match (forged, or from an older tab)
// burns nothing and leaves the login cookie alone, so it cannot cancel a login in progress. The login cookie is
// deleted whenever the flow was burned, and only then: not on 429, not on a state mismatch, not while the
// provider is unavailable before the exchange.
func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r) {
		return
	}
	ctx := r.Context()
	cookie := httpx.CookieValue(r.Header, session.CookieLogin)
	q := r.URL.Query()

	if q.Has(queryError) { // the user refused or Google failed: burn the flow only if the state proves it is ours
		if cookie != "" && h.cfg.Login.Abandon(ctx, cookie, q.Get(queryState)) {
			httpx.ClearCookie(w, session.CookieLogin)
		}
		h.log.Info("google login refused by the provider")
		signInFailed(w)
		return
	}
	res, err := h.cfg.Login.Complete(ctx, googlelogin.CompleteInput{
		CookieValue: cookie, State: q.Get(queryState), Code: q.Get(queryCode),
	})
	if flowBurned(err) {
		httpx.ClearCookie(w, session.CookieLogin)
	}
	var rejected *googlelogin.RejectedError
	switch {
	case err == nil:
	case errors.Is(err, googlelogin.ErrNotReady):
		h.providerUnavailable(w)
		return
	case errors.Is(err, googlelogin.ErrProviderUnavailable):
		// Transient, not a verdict on the user: 503 + Retry-After. The flow is burned, so the user starts again.
		h.log.Warn("google login: provider unavailable", "error", err.Error())
		h.providerUnavailable(w)
		return
	case errors.As(err, &rejected):
		// Contract (Open items): rejections log hd and email; the user sees nothing specific.
		h.log.Warn("google login rejected", "reason", rejected.Reason, "hd", rejected.HD, "email", rejected.Email)
		accessDenied(w)
		return
	case errors.Is(err, googlelogin.ErrInvalidFlow):
		h.log.Warn("google login failed: no matching flow", "error", err.Error())
		signInFailed(w)
		return
	case errors.Is(err, googlelogin.ErrNoCode), errors.Is(err, googlelogin.ErrExchange), errors.Is(err, googlelogin.ErrIDToken):
		h.log.Warn("google login failed: rejected", "error", err.Error())
		signInFailed(w)
		return
	default:
		h.fail(w, err)
		return
	}

	rd, appID, err := h.resolveTarget(ctx, res.RD)
	if err != nil {
		h.fail(w, err)
		return
	}
	id := res.Identity
	userID, active, err := h.cfg.Users.UpsertUser(ctx, id.Subject, id.Email, id.Name)
	if err != nil {
		h.fail(w, err)
		return
	}
	if !active {
		h.log.Warn("google login refused: user is disabled", "user_id", userID)
		accessDenied(w)
		return
	}
	p := loginParams{rd: rd, appID: appID, bindHash: res.BindHash}
	sess, err := h.establishSession(ctx, w, r, userID, map[string]string{"provider": "google", "host": rd.Host})
	if err != nil {
		h.fail(w, err)
		return
	}
	if err := h.redirectWithCode(ctx, w, sess, p); err != nil {
		h.fail(w, err)
		return
	}
	h.log.Info("google login", "user_id", userID, "sid", sess.ID, "host", rd.Host)
}

// flowBurned reports whether the outcome of Complete means the flow was consumed: success, or any failure
// after the atomic consume. Not-ready, a non-matching flow and store failures consumed nothing.
func flowBurned(err error) bool {
	var rejected *googlelogin.RejectedError
	return err == nil || errors.As(err, &rejected) ||
		errors.Is(err, googlelogin.ErrNoCode) || errors.Is(err, googlelogin.ErrExchange) ||
		errors.Is(err, googlelogin.ErrIDToken) || errors.Is(err, googlelogin.ErrProviderUnavailable)
}

func (h *Handler) providerUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(providerRetryAfterSeconds))
	httpx.WritePage(w, http.StatusServiceUnavailable, "Service unavailable", "login provider unavailable")
}

func signInFailed(w http.ResponseWriter) {
	httpx.WritePage(w, http.StatusBadRequest, "Sign-in failed", "The sign-in could not be completed. Go back to the application and try again.")
}

func accessDenied(w http.ResponseWriter) {
	httpx.WritePage(w, http.StatusForbidden, "Access denied", "This account cannot sign in.")
}

// logout handles POST /logout on the auth host.
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if !httpx.SameOrigin(r.Header, h.cfg.AuthHost) {
		httpx.WritePage(w, http.StatusForbidden, "Access denied", "Cross-origin request refused.")
		return
	}
	var rev session.Revoked
	if token := httpx.CookieValue(r.Header, session.CookieSession); token != "" {
		var err error
		if rev, err = h.cfg.Sessions.RevokeSession(r.Context(), token); err != nil {
			h.fail(w, err)
			return
		}
	}
	if rev.SessionID != "" {
		audit.Write(r.Context(), h.cfg.Audit, h.log, audit.Event{
			ActorID: rev.UserID, Action: audit.ActionLogout, TargetType: audit.TargetSession, TargetID: rev.SessionID,
			After: map[string]string{"via": "auth_host"},
		})
	}
	httpx.ClearCookie(w, session.CookieSession)
	w.Header().Set(httpx.HeaderLocation, PathSignedOut)
	w.WriteHeader(http.StatusSeeOther)
	h.log.Info("logout", "user_id", rev.UserID, "sid", rev.SessionID)
}

func (h *Handler) signedOut(w http.ResponseWriter, _ *http.Request) {
	httpx.WritePage(w, http.StatusOK, "Signed out", "You have been signed out.")
}

// currentSession validates the central session cookie; ok is false when there is none or it is not valid.
func (h *Handler) currentSession(ctx context.Context, r *http.Request) (sess session.Session, ok bool, err error) {
	token := httpx.CookieValue(r.Header, session.CookieSession)
	if token == "" {
		return session.Session{}, false, nil
	}
	sess, err = h.cfg.Sessions.ValidateSession(ctx, token)
	if errors.Is(err, session.ErrInvalidSession) {
		return session.Session{}, false, nil
	}
	if err != nil {
		return session.Session{}, false, err
	}
	return sess, true, nil
}

// redirectWithCode issues a login code for the app host of p and redirects the browser to its callback.
func (h *Handler) redirectWithCode(ctx context.Context, w http.ResponseWriter, sess session.Session, p loginParams) error {
	code, err := h.cfg.Sessions.CreateLoginCode(ctx, session.LoginCodeInput{
		SessionID: sess.ID, AppID: p.appID, Host: p.rd.Host, RD: p.rd.URL, BindHash: p.bindHash,
	})
	if err != nil {
		return err
	}
	target := url.URL{Scheme: "https", Host: p.rd.Host, Path: callbackPath, RawQuery: url.Values{"code": {code}}.Encode()}
	w.Header().Set(httpx.HeaderReferrerPolicy, httpx.ReferrerNone)
	w.Header().Set(httpx.HeaderLocation, target.String())
	w.WriteHeader(http.StatusFound)
	return nil
}

// allow applies the per-client rate limit, answering 429 when it is exceeded.
func (h *Handler) allow(w http.ResponseWriter, r *http.Request) bool {
	ok, retry := h.cfg.Limiter.Allow(ratelimit.ClientKey(httpx.ClientIP(r)))
	if ok {
		return true
	}
	secs := int(retry/time.Second) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	httpx.WritePage(w, http.StatusTooManyRequests, "Too many requests", "Please try again in a few seconds.")
	return false
}

// fail maps an error to a page: parameter problems are 400, everything else fails closed with 503.
func (h *Handler) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, errBadRequest) || errors.Is(err, session.ErrInvalidRD) {
		httpx.WritePage(w, http.StatusBadRequest, "Invalid request", "The sign-in destination is not valid.")
		return
	}
	h.log.Error("auth host request failed", "error", err.Error())
	httpx.WritePage(w, http.StatusServiceUnavailable, "Service unavailable", "Please try again later.")
}

func providerMissing(w http.ResponseWriter) {
	httpx.WritePage(w, http.StatusServiceUnavailable, "Service unavailable", "login provider not configured")
}

// parseEmail accepts a plain address (no display name, no comments) within the stored length.
func parseEmail(raw string) (string, bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" || len(raw) > maxEmailLength {
		return "", false
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil || addr.Address != raw || addr.Name != "" {
		return "", false
	}
	return raw, true
}

func localPart(email string) string {
	local, _, _ := strings.Cut(email, "@")
	return local
}

type formData struct{ RD, Bind string }

var loginForm = template.Must(template.New("login").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Sign in</title></head><body>
<h1>Sign in (test mode)</h1>
<form method="post" action="/login">
<input type="hidden" name="rd" value="{{.RD}}"><input type="hidden" name="bind" value="{{.Bind}}">
<label>Email <input type="email" name="email" required maxlength="254"></label>
<button type="submit">Sign in</button>
</form></body></html>`))
