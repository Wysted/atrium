package portalhttp

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"platform/api/internal/portal"
	platformauth "platform/sdk/go"
)

// seenRequest is one request as the Observer received it.
type seenRequest struct {
	method, route string
	status        int
}

type recordingObserver struct {
	mu         sync.Mutex
	requests   []seenRequest
	contention int
}

func (o *recordingObserver) Request(method, route string, status int, _ time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.requests = append(o.requests, seenRequest{method, route, status})
}

func (o *recordingObserver) Contention() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.contention++
}

func observedHandler(t *testing.T, svc Service, obs Observer) http.Handler {
	t.Helper()
	h, err := New(Config{
		Service: svc,
		Admin:   &fakeAdmin{},
		Auth: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sub := r.Header.Get("X-Test-Sub")
				if sub == "" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r.WithContext(platformauth.ContextWithClaims(r.Context(), &platformauth.Claims{Subject: sub})))
			})
		},
		Observer: obs,
		Logger:   discardLog,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// The route reported is the ServeMux pattern, never the request path, and a request stopped before the router is
// "unmatched" instead of carrying whatever the client sent.
func TestObserverGetsPatternsNeverPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		method    string
		path      string
		headers   map[string]string
		wantRoute string
		wantCode  int
	}{
		{"a known route", http.MethodGet, PathMe, nil, "GET " + PathMe, http.StatusOK},
		{"an unknown API path", http.MethodGet, "/api/v1/nope/6f1c", nil, apiPrefix, http.StatusNotFound},
		{"a path of the web UI", http.MethodGet, "/some/page/9d2a", nil, "/", http.StatusNotFound},
		{"rejected by the read isolation", http.MethodGet, "/api/v1/secret-id-123", map[string]string{"Sec-Fetch-Site": "cross-site"}, RouteUnmatched, http.StatusForbidden},
		{"rejected by the authentication", http.MethodGet, "/api/v1/other-secret", map[string]string{"X-Test-Sub": ""}, RouteUnmatched, http.StatusUnauthorized},
		{"an arbitrary method stopped by the content type guard", "BREW", PathMe, nil, RouteUnmatched, http.StatusUnsupportedMediaType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			obs := &recordingObserver{}
			h := observedHandler(t, fakeService{me: portal.Permissions{UserID: callerID}}, obs)
			r := httptest.NewRequestWithContext(t.Context(), tt.method, "https://"+testHost+tt.path, nil)
			r.Host = testHost
			r.Header.Set("X-Test-Sub", callerID)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			for k, v := range tt.headers {
				if v == "" {
					r.Header.Del(k)
					continue
				}
				r.Header.Set(k, v)
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
			if len(obs.requests) != 1 {
				t.Fatalf("%d requests observed, want 1", len(obs.requests))
			}
			got := obs.requests[0]
			if got.route != tt.wantRoute || got.status != tt.wantCode || got.method != tt.method {
				t.Fatalf("observed %+v, want route %q status %d", got, tt.wantRoute, tt.wantCode)
			}
		})
	}
}

func TestObserverCountsContention(t *testing.T) {
	t.Parallel()
	obs := &recordingObserver{}
	svc := fakeService{meErr: fmt.Errorf("grant: %w: %w", portal.ErrContention, errors.New("deadlock"))}
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+testHost+PathMe, nil)
	r.Host = testHost
	r.Header.Set("X-Test-Sub", callerID)
	observedHandler(t, svc, obs).ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || obs.contention != 1 {
		t.Fatalf("status %d, contention %d", w.Code, obs.contention)
	}
	// Other 5xx are not contention.
	obs = &recordingObserver{}
	r = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+testHost+PathMe, nil)
	r.Host = testHost
	r.Header.Set("X-Test-Sub", callerID)
	observedHandler(t, fakeService{meErr: errors.New("boom")}, obs).ServeHTTP(httptest.NewRecorder(), r)
	if obs.contention != 0 || len(obs.requests) != 1 || obs.requests[0].status != http.StatusInternalServerError {
		t.Fatalf("contention %d, requests %+v", obs.contention, obs.requests)
	}
}

func TestNoObserverIsFine(t *testing.T) {
	t.Parallel()
	if w := get(testHandler(t, fakeService{me: portal.Permissions{UserID: callerID}}, nil), PathMe, nil); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
}

// A panic nothing recovered is still reported, as a 500, and the panic goes on.
func TestObserverReportsAPanicAsA500(t *testing.T) {
	t.Parallel()
	obs := &recordingObserver{}
	h := observe(obs, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	func() {
		defer func() { _ = recover() }()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	}()
	if len(obs.requests) != 1 || obs.requests[0].status != http.StatusInternalServerError {
		t.Fatalf("requests = %+v", obs.requests)
	}
}
