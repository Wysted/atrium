// Package portalhttp is the HTTP surface of the portal process (contract block 6): the request-protection
// middleware chain, the response headers, the JSON helpers and the handlers of the API. It depends on the
// application layer (internal/portal) through a small consumer-side interface and knows nothing about SQL.
package portalhttp

import (
	"context"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/httpx"
)

const (
	// apiPrefix is the path prefix of the JSON API. Everything else is the web UI (a later part).
	apiPrefix = "/api/"

	// varyFetchMetadata lists the Fetch Metadata request headers read isolation depends on.
	varyFetchMetadata = "Sec-Fetch-Dest, Sec-Fetch-Mode, Sec-Fetch-Site"

	// Sec-Fetch-Site values a browser sends for a request the user's own page made (or a direct navigation).
	fetchSiteSameOrigin = "same-origin"
	fetchSiteNone       = "none"

	mediaTypeJSON = "application/json"
)

// ReadIsolation protects the reads of /api/*: sibling apps are same-site, so SameSite=Lax cookies reach the
// portal from them and a cross-origin GET could otherwise be answered. A request whose Sec-Fetch-Site is
// anything but same-origin or none (same-site, cross-site or an unknown value) is rejected with 403 before the
// caller is authenticated. A missing header is allowed: a non-browser client, the same choice Go's
// http.CrossOriginProtection makes. Every /api/* response varies on the Fetch Metadata headers and is never
// cached.
func ReadIsolation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, apiPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Vary", varyFetchMetadata)
		h.Set(httpx.HeaderCacheControl, httpx.CacheNoStore)
		switch site := r.Header.Get(httpx.HeaderSecFetchSite); site {
		case "", fetchSiteSameOrigin, fetchSiteNone:
			next.ServeHTTP(w, r)
		default:
			httpx.WriteProblem(w, httpx.Problem{Status: http.StatusForbidden})
		}
	})
}

// CrossOrigin applies http.CrossOriginProtection to the non-safe methods (GET, HEAD and OPTIONS pass):
// same-origin and none pass, any other Sec-Fetch-Site is rejected; without it Origin must match Host (the
// scheme is not compared, which is why HSTS matters); with neither header the request passes. No origin is
// trusted, and a rejection is a 403 problem document.
func CrossOrigin(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusForbidden})
	}))
	return cop.Handler(next)
}

// RequireJSON accepts a non-safe method only with exactly one Content-Type header whose media type is
// application/json (parameters such as charset are allowed); anything else is a 415. This is the last gate
// for a request that carries neither Sec-Fetch-Site nor Origin (an extension, an old client): a form or a
// text/plain fetch cannot send JSON. Clients must send the header on every mutation, DELETE included.
// Handlers never call ParseForm or FormValue.
func RequireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		values := r.Header.Values(httpx.HeaderContentType)
		if len(values) == 1 {
			if mt, _, err := mime.ParseMediaType(values[0]); err == nil && mt == mediaTypeJSON {
				next.ServeHTTP(w, r)
				return
			}
		}
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusUnsupportedMediaType, Detail: "content type must be application/json"})
	})
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// Response headers of every portal response (contract block 6). The Content-Security-Policy set here is the
// one of API responses. The web UI handler (later part) serves HTML and replaces it with the SPA policy, which
// carries the script hashes computed from the embedded HTML at start-up; that is the only place where a CSP
// is overridden.
const (
	// APIContentSecurityPolicy is the policy of JSON responses: nothing may load or frame them.
	APIContentSecurityPolicy = "default-src 'none'; frame-ancestors 'none'"

	hstsValue = "max-age=63072000; includeSubDomains"
)

// SecurityHeaders sets the headers every portal response carries. It never sets Access-Control-* headers:
// the portal is same-origin only and CORS is never enabled.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", APIContentSecurityPolicy)
		h.Set("Strict-Transport-Security", hstsValue)
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set(httpx.HeaderReferrerPolicy, httpx.ReferrerNone)
		h.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// RequestLog writes one log line per request: method, path, status and duration. The query string, headers
// and cookies are never logged.
func RequestLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &httpx.StatusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.StatusOrOK()
		log.Info("request", "method", r.Method, "path", r.URL.Path, "status", status, "duration_ms", time.Since(start).Milliseconds())
	})
}

// routeKey carries a routeHolder through the request context.
type routeKey struct{}

// routeHolder lets the router tell the outermost middleware which pattern served the request. ServeMux sets
// Request.Pattern on the request it hands to the matched handler, which is a copy by the time the guards (deadline,
// authentication) have wrapped the context, so the outer request never sees it.
type routeHolder struct{ pattern string }

// observe reports every request to obs once it is done, whatever stopped it: guards, authentication or a handler.
// A panic that Recover answers is counted as the 503 it writes; only a re-panicked http.ErrAbortHandler is reported as a 500.
func observe(obs Observer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		holder := &routeHolder{}
		rec := &httpx.StatusRecorder{ResponseWriter: w}
		finished := false
		defer func() {
			status := http.StatusInternalServerError
			if finished {
				status = rec.StatusOrOK()
			}
			route := holder.pattern
			if route == "" {
				route = RouteUnmatched
			}
			obs.Request(r.Method, route, status, time.Since(start))
		}()
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), routeKey{}, holder)))
		finished = true
	})
}

// recordRoute wraps the router: after it routed a request, the matched ServeMux pattern goes to the holder of
// observe. The pattern comes from the program's own route table, never from the request.
func recordRoute(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deferred so that a panic, which the recovery middleware outside answers, still leaves its route.
		defer func() {
			if holder, ok := r.Context().Value(routeKey{}).(*routeHolder); ok {
				holder.pattern = r.Pattern
			}
		}()
		mux.ServeHTTP(w, r)
	})
}
