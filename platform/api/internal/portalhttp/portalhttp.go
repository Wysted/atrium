package portalhttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"platform/api/internal/httpx"
	"platform/api/internal/portal"
	platformauth "platform/sdk/go"
)

// Routes of the API (contract block 6).
const (
	PathMe     = "/api/v1/me"
	PathMeApps = "/api/v1/me/apps"
)

// uuidPattern matches the internal user id carried in the assertion's sub. An id that does not parse is
// rejected before it reaches SQL.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// RouteUnmatched is the route reported for a request that never reached the router: one a guard or the
// authentication rejected first.
const RouteUnmatched = "unmatched"

// Observer receives the events metrics count. Nil in Config means none.
type Observer interface {
	// Request reports a finished request. route is the ServeMux pattern that served it (RouteUnmatched when none
	// did), never the request path: the path is client input and would make label cardinality unbounded. method
	// is the request method, which the implementation must bound itself.
	Request(method, route string, status int, elapsed time.Duration)
	// Contention reports a request answered 503 because it lost against concurrent transactions.
	Contention()
}

type noObserver struct{}

func (noObserver) Request(string, string, int, time.Duration) {}
func (noObserver) Contention()                                {}

// Service is the use-case port the handlers call (implemented by portal.Service).
type Service interface {
	Me(ctx context.Context, userID string) (portal.Permissions, error)
	HomeApps(ctx context.Context, userID string) ([]portal.HomeApp, error)
}

// Config describes the public handler of the portal.
type Config struct {
	Service Service
	// Admin serves the administration endpoints (grants, groups, users, audit).
	Admin AdminService
	// Auth wraps a handler with the validation of the Platform-Assertion (the SDK middleware in production).
	// It must put the verified claims in the request context (platformauth.ClaimsFrom).
	Auth func(http.Handler) http.Handler
	// Web serves everything outside /api/ (the SPA, a later part). Nil answers 404 problem documents. A
	// handler that serves HTML must set its own Content-Security-Policy, replacing the API one.
	Web http.Handler
	// Observer counts the requests; nil means none.
	Observer Observer
	// RequestTimeout is the per-request deadline; zero means httpx.RequestTimeout.
	RequestTimeout time.Duration
	Logger         *slog.Logger
}

// New builds the public handler with the middleware chain of the contract, outermost first:
//
//	metrics -> response headers -> request log -> recover -> read isolation (/api/*) -> CrossOriginProtection ->
//	JSON content type (mutations) -> assertion validation (SDK) -> deadline -> routes
//
// The guards run before authentication on purpose: a cross-site request is rejected without learning
// whether it carried a valid assertion.
func New(cfg Config) (http.Handler, error) {
	if cfg.Service == nil || cfg.Admin == nil || cfg.Auth == nil {
		return nil, errors.New("portalhttp: service, admin service and auth are required")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	obs := cfg.Observer
	if obs == nil {
		obs = noObserver{}
	}
	a := &api{svc: cfg.Service, admin: cfg.Admin, obs: obs, log: log}

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+PathMe, a.me)
	mux.HandleFunc("GET "+PathMeApps, a.apps)
	a.adminRoutes(mux)
	mux.HandleFunc(apiPrefix, notFound) // unknown API routes and methods: JSON, never the web UI
	web := cfg.Web
	if web == nil {
		web = http.HandlerFunc(notFound)
	}
	mux.Handle("/", web)

	return observe(obs, protect(cfg.Auth, cfg.RequestTimeout, log, recordRoute(mux))), nil
}

// protect wraps routes with the middleware chain, innermost last.
func protect(auth func(http.Handler) http.Handler, timeout time.Duration, log *slog.Logger, routes http.Handler) http.Handler {
	h := httpx.WithDeadline(timeout, routes)
	h = auth(h)
	h = RequireJSON(h)
	h = CrossOrigin(h)
	h = ReadIsolation(h)
	h = httpx.Recover(log, h)
	h = RequestLog(log, h)
	return SecurityHeaders(h)
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteProblem(w, httpx.Problem{Status: http.StatusNotFound})
}

type api struct {
	svc   Service
	admin AdminService
	obs   Observer
	log   *slog.Logger
}

// caller returns the user id of the verified assertion. The id is the only thing read from it.
func (a *api) caller(w http.ResponseWriter, r *http.Request) (string, bool) {
	claims, ok := platformauth.ClaimsFrom(r.Context())
	if !ok || !uuidPattern.MatchString(claims.Subject) {
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusUnauthorized})
		return "", false
	}
	return strings.ToLower(claims.Subject), true
}

// fail maps an error of a use case to a problem document without leaking internals; the cause goes to the log.
func (a *api) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	switch {
	case errors.Is(err, portal.ErrUserInactive):
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusForbidden})
	case errors.Is(err, portal.ErrForbidden):
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusForbidden})
	case errors.Is(err, portal.ErrNotFound):
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusNotFound})
	case errors.Is(err, portal.ErrInvalidCursor):
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusBadRequest, Detail: detailCursorInvalid})
	case isConflict(err):
		conflict, _ := errors.AsType[*portal.Conflict](err)
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusConflict, Detail: conflict.Detail})
	case isValidation(err):
		v, _ := errors.AsType[*portal.ValidationError](err)
		writeValidation(w, v.Problems)
	case errors.Is(err, portal.ErrContention):
		a.obs.Contention()
		a.log.Warn("request lost to contention", "op", op, "path", r.URL.Path, "error", err.Error())
		w.Header().Set("Retry-After", retryAfterSeconds)
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusServiceUnavailable})
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		a.log.Warn("request ended", "op", op, "path", r.URL.Path, "error", err.Error())
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusServiceUnavailable})
	default:
		a.log.Error("request failed", "op", op, "path", r.URL.Path, "error", err.Error())
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusInternalServerError})
	}
}

func isConflict(err error) bool {
	_, ok := errors.AsType[*portal.Conflict](err)
	return ok
}

func isValidation(err error) bool {
	_, ok := errors.AsType[*portal.ValidationError](err)
	return ok
}

// Fixed details of the problem documents of the administration endpoints.
const (
	detailCursorInvalid    = "the cursor is not valid"
	detailValidationFailed = "the request has invalid fields"
)

// writeValidation answers 422 with the invalid field names and fixed codes; never the input values.
func writeValidation(w http.ResponseWriter, problems []portal.FieldProblem) {
	errs := make([]httpx.FieldError, 0, len(problems))
	for _, p := range problems {
		errs = append(errs, httpx.FieldError{Field: p.Field, Code: p.Code})
	}
	httpx.WriteProblem(w, httpx.Problem{Status: http.StatusUnprocessableEntity, Detail: detailValidationFailed, Errors: errs})
}

// retryAfterSeconds is the Retry-After of a 503 caused by contention: the competing transaction is short.
const retryAfterSeconds = "1"

// Response shapes. Lists are never null.

type initiativeJSON struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type meResponse struct {
	User        userJSON        `json:"user"`
	Permissions permissionsJSON `json:"permissions"`
}

type userJSON struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type permissionsJSON struct {
	PlatformAdmin      bool             `json:"platform_admin"`
	ManagedInitiatives []initiativeJSON `json:"managed_initiatives"`
}

type homeAppJSON struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Hosts      []string       `json:"hosts"`
	Initiative initiativeJSON `json:"initiative"`
	Roles      []string       `json:"roles"`
}

type homeResponse struct {
	Apps []homeAppJSON `json:"apps"`
}

func (a *api) me(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	p, err := a.svc.Me(r.Context(), uid)
	if err != nil {
		a.fail(w, r, "me", err)
		return
	}
	managed := make([]initiativeJSON, 0, len(p.Managed))
	for _, m := range p.Managed {
		managed = append(managed, initiativeJSON(m))
	}
	WriteJSON(w, http.StatusOK, meResponse{
		User:        userJSON{ID: p.UserID, Email: p.Email, Name: p.Name},
		Permissions: permissionsJSON{PlatformAdmin: p.PlatformAdmin, ManagedInitiatives: managed},
	})
}

func (a *api) apps(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	apps, err := a.svc.HomeApps(r.Context(), uid)
	if err != nil {
		a.fail(w, r, "me_apps", err)
		return
	}
	out := homeResponse{Apps: make([]homeAppJSON, 0, len(apps))}
	for _, app := range apps {
		out.Apps = append(out.Apps, homeAppJSON{
			ID: app.ID, Name: app.Name, Hosts: nonNil(app.Hosts), Initiative: initiativeJSON(app.Initiative), Roles: nonNil(app.Roles),
		})
	}
	WriteJSON(w, http.StatusOK, out)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
