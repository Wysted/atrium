package portalhttp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testHost       = "portal.apps.example.test"
	siblingOrigin  = "https://hello.apps.example.test"
	sameOrigin     = "https://" + testHost
	contentJSON    = "application/json"
	contentJSONUTF = "application/json; charset=utf-8"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// probe builds the full chain around routes that accept every method, with an auth step that counts how often
// it ran and answers 401 unless the request carries X-Test-Auth.
type probe struct {
	handler http.Handler
	auth    atomic.Int64 // times the auth step ran
	reached atomic.Int64 // times the routes ran
}

func newProbe(routes http.HandlerFunc) *probe {
	p := &probe{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.reached.Add(1)
		if routes != nil {
			routes(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p.auth.Add(1)
			if r.Header.Get("X-Test-Auth") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	p.handler = protect(auth, time.Second, discardLog, inner)
	return p
}

type request struct {
	method, path string
	headers      map[string]string
	body         string
}

func (p *probe) do(rq request) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), rq.method, "https://"+testHost+rq.path, strings.NewReader(rq.body))
	r.Host = testHost
	r.Header.Set("X-Test-Auth", "yes")
	for k, v := range rq.headers {
		if v == "" {
			r.Header.Del(k)
			continue
		}
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	p.handler.ServeHTTP(w, r)
	return w
}

func TestMutationsAreSameOriginOnly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		headers     map[string]string
		wantStatus  int
		wantReached bool
	}{
		{"same-site", map[string]string{"Sec-Fetch-Site": "same-site", "Content-Type": contentJSON}, http.StatusForbidden, false},
		{"cross-site", map[string]string{"Sec-Fetch-Site": "cross-site", "Content-Type": contentJSON}, http.StatusForbidden, false},
		{"sibling origin without Sec-Fetch-Site", map[string]string{"Origin": siblingOrigin, "Content-Type": contentJSON}, http.StatusForbidden, false},
		{"Sec-Fetch-Site same-origin decides over Origin (pages cannot forge it)", map[string]string{"Origin": siblingOrigin, "Sec-Fetch-Site": "same-origin", "Content-Type": contentJSON}, http.StatusNoContent, true},
		{"same-origin", map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": contentJSON}, http.StatusNoContent, true},
		{"none", map[string]string{"Sec-Fetch-Site": "none", "Content-Type": contentJSON}, http.StatusNoContent, true},
		{"own origin without Sec-Fetch-Site", map[string]string{"Origin": sameOrigin, "Content-Type": contentJSON}, http.StatusNoContent, true},
		{"no fetch metadata and no origin (non-browser client)", map[string]string{"Content-Type": contentJSON}, http.StatusNoContent, true},
		{"text/plain", map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": "text/plain"}, http.StatusUnsupportedMediaType, false},
		{"form post", map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": "application/x-www-form-urlencoded"}, http.StatusUnsupportedMediaType, false},
		{"multipart", map[string]string{"Content-Type": "multipart/form-data; boundary=x"}, http.StatusUnsupportedMediaType, false},
		{"no content type", map[string]string{"Content-Type": ""}, http.StatusUnsupportedMediaType, false},
		{"malformed content type", map[string]string{"Content-Type": "application/json; charset"}, http.StatusUnsupportedMediaType, false},
		{"json look-alike", map[string]string{"Content-Type": "application/jsonp"}, http.StatusUnsupportedMediaType, false},
		{"json with charset", map[string]string{"Content-Type": contentJSONUTF}, http.StatusNoContent, true},
		{"json in capitals", map[string]string{"Content-Type": "Application/JSON"}, http.StatusNoContent, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProbe(nil)
			for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
				p.reached.Store(0)
				w := p.do(request{method: method, path: "/api/v1/things", headers: tt.headers, body: `{}`})
				if w.Code != tt.wantStatus || (p.reached.Load() == 1) != tt.wantReached {
					t.Errorf("%s: status %d reached=%d, want %d reached=%v", method, w.Code, p.reached.Load(), tt.wantStatus, tt.wantReached)
				}
			}
		})
	}
}

func TestRepeatedContentTypeIsRejected(t *testing.T) {
	t.Parallel()
	p := newProbe(nil)
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "https://"+testHost+"/api/v1/things", strings.NewReader("{}"))
	r.Host = testHost
	r.Header.Set("X-Test-Auth", "yes")
	r.Header.Add("Content-Type", contentJSON)
	r.Header.Add("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	p.handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", w.Code)
	}
}

// Read isolation: GET under /api/ from a sibling app (same-site) or another site never reaches the handler.
func TestReadIsolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		path       string
		site       string
		wantStatus int
	}{
		{"api same-site", "/api/v1/me", "same-site", http.StatusForbidden},
		{"api cross-site", "/api/v1/me", "cross-site", http.StatusForbidden},
		{"api unknown value", "/api/v1/me", "weird", http.StatusForbidden},
		{"api same-origin", "/api/v1/me", "same-origin", http.StatusNoContent},
		{"api none", "/api/v1/me", "none", http.StatusNoContent},
		{"api missing header (non-browser)", "/api/v1/me", "", http.StatusNoContent},
		// Only the API is isolated: a link from a sibling app to the portal page is a same-site navigation.
		{"page same-site navigation", "/", "same-site", http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProbe(nil)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				w := p.do(request{method: method, path: tt.path, headers: map[string]string{"Sec-Fetch-Site": tt.site}})
				if w.Code != tt.wantStatus {
					t.Errorf("%s: status = %d, want %d", method, w.Code, tt.wantStatus)
				}
				if strings.HasPrefix(tt.path, apiPrefix) && w.Header().Get("Vary") != varyFetchMetadata {
					t.Errorf("%s: Vary = %q, want %q", method, w.Header().Get("Vary"), varyFetchMetadata)
				}
			}
		})
	}
}

// The guards run before authentication: a request they reject never reaches the auth step, so a cross-site
// page learns nothing about whether its request carried a valid assertion.
func TestGuardsRunBeforeAuthentication(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		req        request
		wantStatus int
		wantAuth   int64
	}{
		{"cross-site read", request{http.MethodGet, "/api/v1/me", map[string]string{"Sec-Fetch-Site": "cross-site", "X-Test-Auth": ""}, ""}, http.StatusForbidden, 0},
		{"cross-site write", request{http.MethodPost, "/api/v1/x", map[string]string{"Sec-Fetch-Site": "same-site", "Content-Type": contentJSON, "X-Test-Auth": ""}, "{}"}, http.StatusForbidden, 0},
		{"cross-site write with a bad content type: origin check first", request{http.MethodPost, "/api/v1/x", map[string]string{"Sec-Fetch-Site": "cross-site", "Content-Type": "text/plain", "X-Test-Auth": ""}, "x"}, http.StatusForbidden, 0},
		{"bad content type", request{http.MethodPost, "/api/v1/x", map[string]string{"Content-Type": "text/plain", "X-Test-Auth": ""}, "x"}, http.StatusUnsupportedMediaType, 0},
		{"same-origin write without assertion", request{http.MethodPost, "/api/v1/x", map[string]string{"Sec-Fetch-Site": "same-origin", "Content-Type": contentJSON, "X-Test-Auth": ""}, "{}"}, http.StatusUnauthorized, 1},
		{"same-origin read without assertion", request{http.MethodGet, "/api/v1/me", map[string]string{"Sec-Fetch-Site": "same-origin", "X-Test-Auth": ""}, ""}, http.StatusUnauthorized, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := newProbe(nil)
			w := p.do(tt.req)
			if w.Code != tt.wantStatus || p.auth.Load() != tt.wantAuth || p.reached.Load() != 0 {
				t.Fatalf("status %d auth=%d reached=%d, want %d auth=%d reached=0", w.Code, p.auth.Load(), p.reached.Load(), tt.wantStatus, tt.wantAuth)
			}
		})
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"Content-Security-Policy":      APIContentSecurityPolicy,
		"Strict-Transport-Security":    hstsValue,
		"X-Frame-Options":              "DENY",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Referrer-Policy":              "no-referrer",
		"X-Content-Type-Options":       "nosniff",
	}
	if !strings.Contains(hstsValue, "includeSubDomains") {
		t.Fatal("HSTS must include subdomains")
	}
	requests := []request{
		{http.MethodGet, "/api/v1/me", map[string]string{"Sec-Fetch-Site": "same-origin"}, ""},
		{http.MethodGet, "/api/v1/me", map[string]string{"Sec-Fetch-Site": "cross-site"}, ""},                     // 403 by isolation
		{http.MethodPost, "/api/v1/x", map[string]string{"Content-Type": "text/plain"}, "x"},                      // 415
		{http.MethodPost, "/api/v1/x", map[string]string{"Sec-Fetch-Site": "same-site", "Content-Type": ""}, "x"}, // 403 by CSRF
		{http.MethodGet, "/", nil, ""},
		{http.MethodGet, "/api/v1/me", map[string]string{"X-Test-Auth": ""}, ""}, // 401 from auth
		{http.MethodOptions, "/api/v1/me", map[string]string{"Origin": siblingOrigin, "Access-Control-Request-Method": "GET"}, ""},
		{http.MethodGet, "/boom", nil, ""}, // handler panic
	}
	p := newProbe(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/boom" {
			panic("boom")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	for _, rq := range requests {
		w := p.do(rq)
		for name, v := range want {
			if got := w.Header().Get(name); got != v {
				t.Errorf("%s %s (%d): %s = %q, want %q", rq.method, rq.path, w.Code, name, got, v)
			}
		}
		for name := range w.Header() {
			if strings.HasPrefix(strings.ToLower(name), "access-control-") {
				t.Errorf("%s %s (%d): response carries %s", rq.method, rq.path, w.Code, name)
			}
		}
	}
	if w := p.do(request{method: http.MethodGet, path: "/boom"}); w.Code != http.StatusServiceUnavailable {
		t.Errorf("panic status = %d, want 503", w.Code)
	}
}

// A web handler replaces the API CSP with its own; everything else stays.
func TestWebHandlerCanSetItsOwnCSP(t *testing.T) {
	t.Parallel()
	const spaCSP = "default-src 'none'; script-src 'self'"
	p := newProbe(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Security-Policy", spaCSP)
	})
	w := p.do(request{method: http.MethodGet, path: "/"})
	if got := w.Header().Get("Content-Security-Policy"); got != spaCSP {
		t.Fatalf("CSP = %q", got)
	}
	if w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("other security headers must survive")
	}
}

func TestAPIResponsesAreNotCacheable(t *testing.T) {
	t.Parallel()
	p := newProbe(nil)
	w := p.do(request{method: http.MethodGet, path: "/api/v1/me"})
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}
