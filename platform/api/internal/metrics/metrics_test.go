package metrics

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"platform/api/internal/access"
	"platform/api/internal/authhost"
	"platform/api/internal/forwardauth"
	"platform/api/internal/probe"
)

// family returns the gathered family called name, or nil.
func family(t *testing.T, reg prometheus.Gatherer, name string) *dto.MetricFamily {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

// value returns the sample of name whose labels are exactly labels (a histogram: its sample count).
func value(t *testing.T, reg prometheus.Gatherer, name string, labels map[string]string) (float64, bool) {
	t.Helper()
	f := family(t, reg, name)
	if f == nil {
		return 0, false
	}
	for _, m := range f.GetMetric() {
		if len(m.GetLabel()) != len(labels) {
			continue
		}
		match := true
		for _, l := range m.GetLabel() {
			if labels[l.GetName()] != l.GetValue() {
				match = false
			}
		}
		if !match {
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
	return 0, false
}

// ownSeries counts the series of the platform_ and portal_ metrics (the runtime collectors are not ours to bound).
func ownSeries(t *testing.T, reg prometheus.Gatherer) int {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range fams {
		if strings.HasPrefix(f.GetName(), "platform_") || strings.HasPrefix(f.GetName(), "portal_") {
			n += len(f.GetMetric())
		}
	}
	return n
}

// lazyPool is a pool that never connects: Stat works without a database.
func lazyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type fakeKeys struct {
	since    time.Time
	loaded   bool
	mismatch bool
}

func (f fakeKeys) ActiveSince() (time.Time, bool) { return f.since, f.loaded }
func (f fakeKeys) KEKMismatch() bool              { return f.mismatch }

func newPlatform(t *testing.T) (*Platform, *prometheus.Registry) {
	t.Helper()
	reg, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPlatform(reg)
	if err != nil {
		t.Fatal(err)
	}
	return p, reg
}

func randomText(r *rand.Rand) string {
	return fmt.Sprintf("hostile-%x.%x.example", r.Uint64(), r.Uint64())
}

// The registry is the process's own: nothing lands in the global default one.
func TestRegistryIsNeverTheDefault(t *testing.T) {
	t.Parallel()
	p, reg := newPlatform(t)
	p.Login(authhost.LoginSuccess)
	if family(t, reg, "platform_logins_total") == nil {
		t.Fatal("the metric is missing from the process registry")
	}
	fams, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fams {
		if strings.HasPrefix(f.GetName(), "platform_") || strings.HasPrefix(f.GetName(), "portal_") {
			t.Fatalf("%s leaked into the default registry", f.GetName())
		}
	}
	// Two registries in one process do not collide: every app of a test run builds its own.
	if _, err := NewPlatform(mustRegistry(t)); err != nil {
		t.Fatalf("second platform: %v", err)
	}
}

func mustRegistry(t *testing.T) *prometheus.Registry {
	t.Helper()
	reg, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestPlatformExposesTheCatalogWithItsTypes(t *testing.T) {
	t.Parallel()
	p, reg := newPlatform(t)
	since := time.Now().Add(-2 * time.Hour)
	err := RegisterPlatformState(reg, PlatformSources{
		Pool: lazyPool(t), Keys: fakeKeys{since: since, loaded: true},
		Pending: func() int { return 3 },
		UnknownDrops: func() forwardauth.UnknownHostDrops {
			return forwardauth.UnknownHostDrops{Invalid: 1, Budget: 2, Marker: 3, Queue: 4}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p.Decision(forwardauth.DecisionAllow, access.ReasonGranted, time.Millisecond)
	p.CacheLookup(forwardauth.CacheHost, true)
	p.Login(authhost.LoginSuccess)
	p.AppLogout()
	p.AuthHostLogout()
	p.FlushFailed()
	rec := httptest.NewRecorder()
	p.CountJWKS(ListenerInternal, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).
		ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	want := map[string]dto.MetricType{
		"platform_forwardauth_decisions_total":           dto.MetricType_COUNTER,
		"platform_forwardauth_decision_duration_seconds": dto.MetricType_HISTOGRAM,
		"platform_forwardauth_cache_lookups_total":       dto.MetricType_COUNTER,
		"platform_logins_total":                          dto.MetricType_COUNTER,
		"platform_logouts_total":                         dto.MetricType_COUNTER,
		"platform_jwks_requests_total":                   dto.MetricType_COUNTER,
		"platform_lastseen_flush_errors_total":           dto.MetricType_COUNTER,
		"platform_lastseen_pending_hosts":                dto.MetricType_GAUGE,
		"platform_unknown_host_drops_total":              dto.MetricType_COUNTER,
		"platform_signing_key_loaded":                    dto.MetricType_GAUGE,
		"platform_signing_key_active_age_seconds":        dto.MetricType_GAUGE,
		"platform_signing_key_kek_mismatch":              dto.MetricType_GAUGE,
		"platform_db_pool_acquired_connections":          dto.MetricType_GAUGE,
		"platform_db_pool_idle_connections":              dto.MetricType_GAUGE,
		"platform_db_pool_total_connections":             dto.MetricType_GAUGE,
		"platform_db_pool_max_connections":               dto.MetricType_GAUGE,
		"platform_db_pool_acquires_total":                dto.MetricType_COUNTER,
		"platform_db_pool_acquires_waited_total":         dto.MetricType_COUNTER,
		"platform_db_pool_acquire_wait_seconds_total":    dto.MetricType_COUNTER,
		"platform_db_pool_acquires_canceled_total":       dto.MetricType_COUNTER,
		"go_goroutines":                                  dto.MetricType_GAUGE,
		"process_open_fds":                               dto.MetricType_GAUGE,
	}
	for name, typ := range want {
		f := family(t, reg, name)
		switch {
		case f == nil && name == "process_open_fds":
			// The process collector is Linux-only in practice; the other collectors prove the registration.
		case f == nil:
			t.Errorf("%s is not exposed", name)
		case f.GetType() != typ:
			t.Errorf("%s is a %s, want %s", name, f.GetType(), typ)
		}
	}

	checks := []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"platform_forwardauth_decisions_total", map[string]string{"decision": "allow", "reason": "granted"}, 1},
		{"platform_forwardauth_cache_lookups_total", map[string]string{"cache": "host", "result": "hit"}, 1},
		{"platform_forwardauth_cache_lookups_total", map[string]string{"cache": "host", "result": "miss"}, 0},
		{"platform_logins_total", map[string]string{"outcome": "success"}, 1},
		{"platform_logins_total", map[string]string{"outcome": "rejected"}, 0},
		{"platform_logouts_total", map[string]string{"via": "app"}, 1},
		{"platform_logouts_total", map[string]string{"via": "auth_host"}, 1},
		{"platform_jwks_requests_total", map[string]string{"listener": "internal", "status_class": "2xx"}, 1},
		{"platform_lastseen_flush_errors_total", map[string]string{}, 1},
		{"platform_lastseen_pending_hosts", map[string]string{}, 3},
		{"platform_unknown_host_drops_total", map[string]string{"reason": "invalid"}, 1},
		{"platform_unknown_host_drops_total", map[string]string{"reason": "budget"}, 2},
		{"platform_unknown_host_drops_total", map[string]string{"reason": "marker"}, 3},
		{"platform_unknown_host_drops_total", map[string]string{"reason": "queue"}, 4},
		{"platform_signing_key_loaded", map[string]string{}, 1},
		{"platform_signing_key_kek_mismatch", map[string]string{}, 0},
	}
	for _, c := range checks {
		if got, ok := value(t, reg, c.name, c.labels); !ok || got != c.want {
			t.Errorf("%s%v = %v (present %v), want %v", c.name, c.labels, got, ok, c.want)
		}
	}
	if age, ok := value(t, reg, "platform_signing_key_active_age_seconds", map[string]string{}); !ok || age < 7190 || age > 7300 {
		t.Errorf("active key age = %v (present %v), want about 2h", age, ok)
	}
	if n, ok := value(t, reg, "platform_forwardauth_decision_duration_seconds", map[string]string{"decision": "allow"}); !ok || n != 1 {
		t.Errorf("decision duration samples = %v (present %v)", n, ok)
	}
}

func TestSigningKeyStates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		keys         fakeKeys
		wantLoaded   float64
		wantMismatch float64
		wantAge      bool
	}{
		{"loaded", fakeKeys{since: time.Now(), loaded: true}, 1, 0, true},
		{"not loaded", fakeKeys{}, 0, 0, false},
		{"KEK mismatch", fakeKeys{mismatch: true}, 0, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := mustRegistry(t)
			if err := RegisterPlatformState(reg, PlatformSources{Pool: lazyPool(t), Keys: tt.keys}); err != nil {
				t.Fatal(err)
			}
			if got, _ := value(t, reg, "platform_signing_key_loaded", nil); got != tt.wantLoaded {
				t.Errorf("loaded = %v, want %v", got, tt.wantLoaded)
			}
			if got, _ := value(t, reg, "platform_signing_key_kek_mismatch", nil); got != tt.wantMismatch {
				t.Errorf("kek mismatch = %v, want %v", got, tt.wantMismatch)
			}
			if _, ok := value(t, reg, "platform_signing_key_active_age_seconds", nil); ok != tt.wantAge {
				t.Errorf("age present = %v, want %v", ok, tt.wantAge)
			}
		})
	}
}

// Whatever a caller passes, the series stay inside the closed sets: values outside them become "other".
func TestPlatformLabelsAreClamped(t *testing.T) {
	t.Parallel()
	p, reg := newPlatform(t)
	p.Decision("hostile-decision", "hostile-reason", time.Millisecond)
	p.CacheLookup("hostile-cache", false)
	p.Login("hostile-outcome")
	rec := httptest.NewRecorder()
	p.CountJWKS("hostile-listener", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })).
		ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if got, ok := value(t, reg, "platform_forwardauth_decisions_total", map[string]string{"decision": "other", "reason": "other"}); !ok || got != 1 {
		t.Errorf("decision other/other = %v (present %v)", got, ok)
	}
	if got, ok := value(t, reg, "platform_forwardauth_cache_lookups_total", map[string]string{"cache": "other", "result": "miss"}); !ok || got != 1 {
		t.Errorf("cache other/miss = %v (present %v)", got, ok)
	}
	if got, ok := value(t, reg, "platform_logins_total", map[string]string{"outcome": "other"}); !ok || got != 1 {
		t.Errorf("login other = %v (present %v)", got, ok)
	}
	if got, ok := value(t, reg, "platform_jwks_requests_total", map[string]string{"listener": "other", "status_class": "4xx"}); !ok || got != 1 {
		t.Errorf("jwks other/4xx = %v (present %v)", got, ok)
	}
}

// The number of series does not grow with the variety of the input.
func TestPlatformSeriesDoNotGrowWithInput(t *testing.T) {
	t.Parallel()
	p, reg := newPlatform(t)
	exercise := func(decision, reason, cache, outcome, listener string) {
		p.Decision(decision, reason, time.Microsecond)
		p.CacheLookup(cache, true)
		p.Login(outcome)
		p.CountJWKS(listener, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
			ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	}
	exercise("hostile", "hostile", "hostile", "hostile", "hostile") // creates the "other" series
	before := ownSeries(t, reg)
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // G404: reproducible hostile input, not a secret
	const attempts = 5000
	for range attempts {
		exercise(randomText(r), randomText(r), randomText(r), randomText(r), randomText(r))
	}
	if after := ownSeries(t, reg); after != before {
		t.Fatalf("series grew from %d to %d after %d requests with random label values", before, after, attempts)
	}
}

func TestPortalSeriesDoNotGrowWithInput(t *testing.T) {
	t.Parallel()
	reg := mustRegistry(t)
	p, err := NewPortal(reg, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p.Request("GET", "GET /api/v1/me", http.StatusOK, time.Millisecond)
	p.Request("BREW", "unmatched", http.StatusTeapot, time.Millisecond)
	p.Request("GET", "", 1234, time.Millisecond) // an empty route and a status outside any class
	before := ownSeries(t, reg)
	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // G404: reproducible hostile input, not a secret
	for range 5000 {
		// The route is a pattern chosen by the program; the method and the status are the inputs that vary.
		p.Request(randomText(r), "unmatched", r.IntN(2000), time.Millisecond)
		p.Round(randomText(r), time.Second)
	}
	// "other" is a value of the method and status class labels: the series it creates are bounded by the product of
	// the sets, so the first pass above may add them but a second pass of the same size must not add more.
	mid := ownSeries(t, reg)
	for range 5000 {
		p.Request(randomText(r), "unmatched", r.IntN(2000), time.Millisecond)
		p.Round(randomText(r), time.Second)
	}
	if after := ownSeries(t, reg); after != mid {
		t.Fatalf("series grew from %d to %d on the second pass", mid, after)
	}
	if mid-before > 12 {
		t.Fatalf("%d new series from random methods and statuses, want at most the 'other' method times the status classes", mid-before)
	}
}

// fakeCounter answers the deployments by probe status and counts the queries.
type fakeCounter struct {
	counts map[probe.Status]int
	err    error
	calls  int
}

func (f *fakeCounter) StatusCounts(context.Context) (map[probe.Status]int, error) {
	f.calls++
	return f.counts, f.err
}

func TestPortalCatalog(t *testing.T) {
	t.Parallel()
	reg := mustRegistry(t)
	p, err := NewPortal(reg, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	counter := &fakeCounter{counts: map[probe.Status]int{probe.StatusOK: 4, probe.StatusUnprotected: 1}}
	if err := RegisterPortalState(reg, PortalSources{Pool: lazyPool(t), ProbeStatus: counter}); err != nil {
		t.Fatal(err)
	}
	p.Request(http.MethodGet, "GET /api/v1/me", http.StatusOK, 3*time.Millisecond)
	p.Request(http.MethodGet, "GET /api/v1/me", http.StatusServiceUnavailable, 3*time.Millisecond)
	p.Contention()
	p.Round(probe.RoundWritten, 2*time.Second)

	want := map[string]dto.MetricType{
		"portal_http_requests_total":                dto.MetricType_COUNTER,
		"portal_http_request_duration_seconds":      dto.MetricType_HISTOGRAM,
		"portal_contention_responses_total":         dto.MetricType_COUNTER,
		"portal_probe_rounds_total":                 dto.MetricType_COUNTER,
		"portal_probe_round_duration_seconds":       dto.MetricType_HISTOGRAM,
		"portal_probe_interval_seconds":             dto.MetricType_GAUGE,
		"portal_probe_last_write_timestamp_seconds": dto.MetricType_GAUGE,
		"portal_deployments_by_probe_status":        dto.MetricType_GAUGE,
		"portal_probe_status_count_errors_total":    dto.MetricType_COUNTER,
		"portal_db_pool_total_connections":          dto.MetricType_GAUGE,
		"portal_db_pool_acquire_wait_seconds_total": dto.MetricType_COUNTER,
	}
	for name, typ := range want {
		if f := family(t, reg, name); f == nil || f.GetType() != typ {
			t.Errorf("%s: family %v, want type %s", name, f, typ)
		}
	}
	checks := []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"portal_http_requests_total", map[string]string{"method": "GET", "route": "GET /api/v1/me", "status_class": "2xx"}, 1},
		{"portal_http_requests_total", map[string]string{"method": "GET", "route": "GET /api/v1/me", "status_class": "5xx"}, 1},
		{"portal_http_request_duration_seconds", map[string]string{"route": "GET /api/v1/me"}, 2},
		{"portal_contention_responses_total", map[string]string{}, 1},
		{"portal_probe_rounds_total", map[string]string{"result": "written"}, 1},
		{"portal_probe_rounds_total", map[string]string{"result": "idle"}, 0},
		{"portal_probe_round_duration_seconds", map[string]string{"result": "written"}, 1},
		{"portal_probe_interval_seconds", map[string]string{}, 300},
		{"portal_deployments_by_probe_status", map[string]string{"status": "ok"}, 4},
		{"portal_deployments_by_probe_status", map[string]string{"status": "unprotected"}, 1},
		{"portal_deployments_by_probe_status", map[string]string{"status": "never"}, 0},
	}
	for _, c := range checks {
		if got, ok := value(t, reg, c.name, c.labels); !ok || got != c.want {
			t.Errorf("%s%v = %v (present %v), want %v", c.name, c.labels, got, ok, c.want)
		}
	}
}

func TestProbeLastWriteTimestamp(t *testing.T) {
	t.Parallel()
	const name = "portal_probe_last_write_timestamp_seconds"
	const tolerance = 5.0
	reg := mustRegistry(t)
	before := float64(time.Now().UnixNano()) / float64(time.Second)
	p, err := NewPortal(reg, 0)
	if err != nil {
		t.Fatal(err)
	}
	start, ok := value(t, reg, name, map[string]string{})
	if !ok || start < before-tolerance || start > before+tolerance {
		t.Fatalf("%s after startup = %v (present %v), want about %v", name, start, ok, before)
	}
	if f := family(t, reg, name); f.GetType() != dto.MetricType_GAUGE {
		t.Fatalf("%s type = %v, want gauge", name, f.GetType())
	}

	written := time.Unix(1_900_000_000, 500_000_000)
	p.now = func() time.Time { return written }
	for _, r := range []string{probe.RoundSkipped, probe.RoundError, probe.RoundIdle, probe.RoundInconclusive} {
		p.Round(r, time.Second)
		if got, _ := value(t, reg, name, map[string]string{}); got != start {
			t.Fatalf("%s moved to %v on a %q round, want %v", name, got, r, start)
		}
	}
	p.Round(probe.RoundWritten, time.Second)
	if got, _ := value(t, reg, name, map[string]string{}); got != 1_900_000_000.5 {
		t.Fatalf("%s after a written round = %v, want 1.9000000005e9", name, got)
	}
}

func TestProbeStatusIsReadAtScrapeTimeWithACache(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	counter := &fakeCounter{counts: map[probe.Status]int{probe.StatusOK: 2}}
	reg := mustRegistry(t)
	err := RegisterPortalState(reg, PortalSources{Pool: lazyPool(t), ProbeStatus: counter, TTL: 30 * time.Second, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	status := func(s string) (float64, bool) {
		return value(t, reg, "portal_deployments_by_probe_status", map[string]string{"status": s})
	}

	for range 3 { // three scrapes inside the TTL: one query
		if got, ok := status("ok"); !ok || got != 2 {
			t.Fatalf("ok = %v (present %v), want 2", got, ok)
		}
	}
	if counter.calls != 1 {
		t.Fatalf("%d queries for 3 scrapes inside the TTL, want 1", counter.calls)
	}

	// After the TTL the database state wins: a status that dropped to zero is 0, not its old value.
	now = now.Add(31 * time.Second)
	counter.counts = map[probe.Status]int{probe.StatusUnprotected: 5}
	if got, _ := status("ok"); got != 0 {
		t.Fatalf("ok = %v after the TTL, want 0", got)
	}
	if got, _ := status("unprotected"); got != 5 || counter.calls != 2 {
		t.Fatalf("unprotected = %v after %d queries", got, counter.calls)
	}

	// A failed count exposes nothing (no stale value), counts the error, and is not retried inside the TTL.
	now = now.Add(31 * time.Second)
	counter.err = errors.New("database down")
	if _, ok := status("ok"); ok {
		t.Fatal("a failed count still exposed a value")
	}
	status("ok")
	if got, _ := value(t, reg, "portal_probe_status_count_errors_total", nil); got != 1 || counter.calls != 3 {
		t.Fatalf("errors = %v after %d queries, want 1 error and 3 queries", got, counter.calls)
	}

	// Recovery after the TTL.
	now = now.Add(31 * time.Second)
	counter.err = nil
	if got, ok := status("unprotected"); !ok || got != 5 {
		t.Fatalf("unprotected = %v (present %v) after recovery", got, ok)
	}
}

func TestHandlerServesThePrometheusTextFormat(t *testing.T) {
	t.Parallel()
	p, reg := newPlatform(t)
	p.Login(authhost.LoginSuccess)
	p.Decision(forwardauth.DecisionAllow, access.ReasonGranted, time.Millisecond)
	rec := httptest.NewRecorder()
	Handler(reg).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, Path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content type %q", ct)
	}
	body := rec.Body.String()
	for _, line := range []string{
		"# TYPE platform_logins_total counter",
		`platform_logins_total{outcome="success"} 1`,
		"# TYPE platform_forwardauth_decision_duration_seconds histogram",
		"# TYPE go_goroutines gauge",
	} {
		if !strings.Contains(body, line) {
			t.Errorf("exposition lacks %q", line)
		}
	}
}
