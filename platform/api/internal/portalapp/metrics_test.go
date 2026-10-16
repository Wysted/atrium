package portalapp_test

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"platform/api/internal/probe"
)

// sample returns the value of name{labels} (a histogram: its sample count) and whether the series exists.
func (s *stack) sample(name string, labels map[string]string) (float64, bool) {
	s.t.Helper()
	fams, err := s.app.Metrics.Gather()
	if err != nil {
		s.t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if !sameLabels(m, labels) {
				continue
			}
			switch {
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue(), true
			case m.GetGauge() != nil:
				return m.GetGauge().GetValue(), true
			case m.GetHistogram() != nil:
				return float64(m.GetHistogram().GetSampleCount()), true
			}
		}
	}
	return 0, false
}

func sameLabels(m *dto.Metric, want map[string]string) bool {
	if len(m.GetLabel()) != len(want) {
		return false
	}
	for _, l := range m.GetLabel() {
		if want[l.GetName()] != l.GetValue() {
			return false
		}
	}
	return true
}

func (s *stack) ownSeries() int {
	s.t.Helper()
	fams, err := s.app.Metrics.Gather()
	if err != nil {
		s.t.Fatal(err)
	}
	n := 0
	for _, f := range fams {
		if strings.HasPrefix(f.GetName(), "portal_") {
			n += len(f.GetMetric())
		}
	}
	return n
}

func (s *stack) internal(method, path string) *httptest.ResponseRecorder {
	s.t.Helper()
	w := httptest.NewRecorder()
	s.app.Internal.ServeHTTP(w, httptest.NewRequestWithContext(s.t.Context(), method, path, nil))
	return w
}

func (s *stack) metricsListener(method, path string) *httptest.ResponseRecorder {
	s.t.Helper()
	w := httptest.NewRecorder()
	s.app.MetricsHandler.ServeHTTP(w, httptest.NewRequestWithContext(s.t.Context(), method, path, nil))
	return w
}

// /metrics is on its own listener: the public and the internal listener answer 404, whoever asks.
func TestMetricsHaveTheirOwnListener(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	uid := s.seed()
	valid := s.assertion(uid, portalAppID)

	s.public(call{method: http.MethodGet, path: "/api/v1/me", assertion: valid})
	w := s.metricsListener(http.MethodGet, "/metrics")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics listener: status %d, content type %q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		"# TYPE portal_http_requests_total counter",
		"# TYPE portal_http_request_duration_seconds histogram",
		"# TYPE portal_db_pool_total_connections gauge",
		"# TYPE portal_probe_rounds_total counter",
		"# TYPE portal_deployments_by_probe_status gauge",
		"# TYPE go_goroutines gauge",
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("exposition lacks %q", want)
		}
	}
	if w := s.metricsListener(http.MethodPost, "/metrics"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /metrics = %d, want 405", w.Code)
	}
	if w := s.metricsListener(http.MethodGet, "/readyz"); w.Code != http.StatusNotFound {
		t.Errorf("the metrics listener serves /readyz (%d)", w.Code)
	}

	if w := s.internal(http.MethodGet, "/metrics"); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "portal_") {
		t.Errorf("internal /metrics = %d, want 404", w.Code)
	}
	// Public: with a valid assertion the router has no such route (the web handler is absent), and without one
	// the assertion check stops it. Neither leaks anything.
	for name, c := range map[string]call{
		"with a valid assertion": {method: http.MethodGet, path: "/metrics", assertion: valid},
		"without an assertion":   {method: http.MethodGet, path: "/metrics"},
	} {
		w := s.public(c)
		if w.Code != http.StatusNotFound && w.Code != http.StatusUnauthorized {
			t.Errorf("public /metrics %s = %d, want 404 or 401", name, w.Code)
		}
		if strings.Contains(w.Body.String(), "portal_") || strings.Contains(w.Body.String(), "go_goroutines") {
			t.Errorf("public /metrics %s leaked metrics", name)
		}
	}
	if w := s.public(call{method: http.MethodGet, path: "/metrics", assertion: valid}); w.Code != http.StatusNotFound {
		t.Errorf("public /metrics with a valid assertion = %d, want 404", w.Code)
	}
}

func TestMetricsCountRequestsByRoutePattern(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	uid := s.seed()
	valid := s.assertion(uid, portalAppID)

	for range 2 {
		s.public(call{method: http.MethodGet, path: "/api/v1/me", assertion: valid})
	}
	s.public(call{method: http.MethodGet, path: "/api/v1/me"})                                                             // 401
	s.public(call{method: http.MethodGet, path: "/api/v1/nope/" + uid, assertion: valid})                                  // 404
	s.public(call{method: http.MethodGet, path: "/api/v1/me", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}}) // 403

	for _, c := range []struct {
		labels map[string]string
		want   float64
	}{
		{map[string]string{"method": "GET", "route": "GET /api/v1/me", "status_class": "2xx"}, 2},
		{map[string]string{"method": "GET", "route": "unmatched", "status_class": "4xx"}, 2}, // 401 and 403: stopped before the router
		{map[string]string{"method": "GET", "route": "/api/", "status_class": "4xx"}, 1},
	} {
		if got, ok := s.sample("portal_http_requests_total", c.labels); !ok || got != c.want {
			t.Errorf("requests%v = %v (present %v), want %v", c.labels, got, ok, c.want)
		}
	}
	if got, ok := s.sample("portal_http_request_duration_seconds", map[string]string{"route": "GET /api/v1/me"}); !ok || got != 2 {
		t.Errorf("duration samples for /api/v1/me = %v (present %v), want 2", got, ok)
	}
	if got, ok := s.sample("portal_db_pool_max_connections", map[string]string{}); !ok || got < 1 {
		t.Errorf("pool max connections = %v (present %v)", got, ok)
	}
	if got, ok := s.sample("portal_db_pool_acquires_total", map[string]string{}); !ok || got < 1 {
		t.Errorf("pool acquires = %v (present %v), the requests used the pool", got, ok)
	}
}

// The probe observer handed out for the probe job feeds the registry of the same app, and the deployments by probe
// status come from the database even though no probe job runs in this app.
func TestProbeMetrics(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	s.seed()
	s.app.ProbeObserver.Round(probe.RoundWritten, 3*time.Second)
	s.exec(`UPDATE deployments SET last_probe_status = 'ok', last_probe_at = now() WHERE host = $1`, portalHost)
	if got, ok := s.sample("portal_probe_rounds_total", map[string]string{"result": "written"}); !ok || got != 1 {
		t.Errorf("written rounds = %v (present %v)", got, ok)
	}
	for status, want := range map[string]float64{"ok": 1, "never": 1, "unprotected": 0} {
		if got, ok := s.sample("portal_deployments_by_probe_status", map[string]string{"status": status}); !ok || got != want {
			t.Errorf("deployments %s = %v (present %v), want %v", status, got, ok, want)
		}
	}
}

// Paths, methods, queries and hosts are client input: the number of series after thousands of them equals the
// number after one of each kind, and none of their values is exposed.
func TestMetricsCardinalityDoesNotGrowWithHostileInput(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	uid := s.seed()
	valid := s.assertion(uid, portalAppID)
	r := rand.New(rand.NewPCG(5, 9)) //nolint:gosec // G404: reproducible hostile input, not a secret
	const sentinel = "zz-hostile-"
	hex := func() string { return fmt.Sprintf("%016x", r.Uint64()) }

	flood := func(i int) {
		h := map[string]string{}
		path := "/" + sentinel + hex()
		switch i % 6 {
		case 0: // random web path, authenticated
			s.public(call{method: http.MethodGet, path: path + "/" + hex(), assertion: valid})
		case 1: // random API path, authenticated
			s.public(call{method: http.MethodGet, path: "/api/v1/" + sentinel + hex(), assertion: valid})
		case 2: // random method (stopped by the content type guard, so before the router)
			s.public(call{method: "X" + strings.ToUpper(hex()), path: path, assertion: valid})
		case 3: // random method with a JSON body: reaches the router
			h["Content-Type"] = "application/json"
			s.public(call{method: "X" + strings.ToUpper(hex()), path: path, assertion: valid, headers: h, body: "{}"})
		case 4: // unauthenticated, random query and path
			s.public(call{method: http.MethodGet, path: path + "?" + sentinel + hex() + "=" + hex()})
		case 5: // random path on the internal listener
			s.internal(http.MethodGet, path)
		}
	}
	for i := range 6 {
		flood(i)
	}
	before := s.ownSeries()
	const requests = 1200
	for i := range requests {
		flood(i)
	}
	if after := s.ownSeries(); after != before {
		t.Fatalf("series grew from %d to %d after %d hostile requests", before, after, requests)
	}
	if body := s.metricsListener(http.MethodGet, "/metrics").Body.String(); strings.Contains(strings.ToLower(body), sentinel) {
		t.Fatal("a value supplied by the client appears in the exposition")
	}
}
