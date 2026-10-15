// Package authhost serves the browser routes of the auth host (public listener): /login, /logout and the
// signed-out page. Google login arrives in a later part; until then an explicit, off-by-default fake login
// lets the rest of the flow run end to end.
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
	"platform/api/internal/httpx"
	"platform/api/internal/ratelimit"
	"platform/api/internal/session"
)

const (
	// Paths of the auth host.
	PathLogin     = "/login"
	PathLogout    = "/logout"
	PathSignedOut = "/signed-out"

	// callbackPath is the reserved path of every app host (contract block 2).
	callbackPath = "/_platform/callback"

	queryRD   = "rd"
	queryBind = "bind"
	fieldMail = "email"

	// fakeSubPrefix namespaces the google_sub of fake users so they can never collide with a real one.
	fakeSubPrefix = "fake:"
	// maxEmailLength matches the users.email CHECK.
	maxEmailLength = 254
	// maxFormBytes bounds the fake login form body.
	maxFormBytes = 8 << 10
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

// Limiter decides whether a client may call /login now.
type Limiter interface {
	Allow(key string) (ok bool, retryAfter time.Duration)
}

// Config wires a Handler. RequestTimeout (default httpx.RequestTimeout) and Logger are optional.
type Config struct {
	AuthHost       string
	Sessions       Sessions
	Apps           Apps
	Users          Users
	Limiter        Limiter
	Audit          audit.Recorder
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
	return h, nil
}

// Register adds the routes to mux. /oauth2/callback is intentionally absent (404) until the Google part.
// Every route runs under a request deadline, so a hung database ends in the explicit 503 page.
func (h *Handler) Register(mux *http.ServeMux) {
	route := func(f http.HandlerFunc) http.Handler {
		return httpx.WithDeadline(h.cfg.RequestTimeout, httpx.WithPageHeaders(f))
	}
	mux.Handle("GET "+PathLogin, route(h.loginPage))
	mux.Handle("POST "+PathLogin, route(h.fakeLogin))
	mux.Handle("POST "+PathLogout, route(h.logout))
	mux.Handle("GET "+PathSignedOut, route(h.signedOut))
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
	rd, err := h.cfg.Sessions.ValidateRD(ctx, rawRD)
	if errors.Is(err, session.ErrInvalidRD) {
		return loginParams{}, errBadRequest
	}
	if err != nil {
		return loginParams{}, err
	}
	bind, err := base64.RawURLEncoding.Strict().DecodeString(rawBind)
	if err != nil || len(bind) != session.TokenBytes {
		return loginParams{}, errBadRequest
	}
	appID, found, err := h.cfg.Apps.AppForHost(ctx, rd.Host)
	if err != nil {
		return loginParams{}, err
	}
	if !found {
		return loginParams{}, errBadRequest
	}
	return loginParams{rd: rd, appID: appID, bindHash: bind}, nil
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
	userID, active, err := h.cfg.Users.UpsertUser(ctx, fakeSubPrefix+email, email, localPart(email))
	if err != nil {
		h.fail(w, err)
		return
	}
	if !active {
		httpx.WritePage(w, http.StatusForbidden, "Access denied", "This account is disabled.")
		return
	}
	// A new login never keeps the previous central session (no fixation, no leftover app sessions).
	if old := httpx.CookieValue(r.Header, session.CookieSession); old != "" {
		prev, err := h.cfg.Sessions.RevokeSession(ctx, old)
		if err != nil {
			h.fail(w, err)
			return
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
		h.fail(w, err)
		return
	}
	// The session exists from here on: audit it before anything else can fail.
	audit.Write(ctx, h.cfg.Audit, h.log, audit.Event{
		ActorID: userID, Action: audit.ActionLogin, TargetType: audit.TargetSession, TargetID: sess.ID,
		After: map[string]string{"method": "fake", "host": p.rd.Host},
	})
	httpx.SetCookie(w, session.CookieSession, token, time.Until(sess.ExpiresAt))
	if err := h.redirectWithCode(ctx, w, sess, p); err != nil {
		h.fail(w, err)
		return
	}
	h.log.Warn("fake login", "user_id", userID, "sid", sess.ID, "host", p.rd.Host)
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
