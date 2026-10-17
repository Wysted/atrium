package app_test

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/Wysted/atrium/platform/api/internal/jwks"
	"github.com/Wysted/atrium/platform/api/internal/keys"
)

// sample returns the value of the series name{labels} of the app's registry (a histogram: its sample count), and
// whether it exists. labels must be all the labels of the series.
func (e *env) sample(name string, labels map[string]string) (float64, bool) {
	e.t.Helper()
	fams, err := e.app.Metrics.Gather()
	if err != nil {
		e.t.Fatal(err)
	}
	for _, f := range fams {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if labelsEqual(m, labels) {
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
	}
	return 0, false
}

func labelsEqual(m *dto.Metric, want map[string]string) bool {
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

// metric is sample for a series that must exist.
func (e *env) metric(name string, labels map[string]string) float64 {
	e.t.Helper()
	v, ok := e.sample(name, labels)
	if !ok {
		e.t.Fatalf("series %s%v does not exist", name, labels)
	}
	return v
}

// ownSeries counts the platform_ series of the registry.
func (e *env) ownSeries() int {
	e.t.Helper()
	fams, err := e.app.Metrics.Gather()
	if err != nil {
		e.t.Fatal(err)
	}
	n := 0
	for _, f := range fams {
		if strings.HasPrefix(f.GetName(), "platform_") {
			n += len(f.GetMetric())
		}
	}
	return n
}

func (e *env) get(base, path string) result {
	e.t.Helper()
	req, err := http.NewRequestWithContext(e.t.Context(), http.MethodGet, base+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.newBrowser().do(req)
}

// /metrics has a listener of its own: the internal one (ForwardAuth, probes), the JWKS one and the public one answer 404.
func TestMetricsHaveTheirOwnListener(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.newBrowser().forward(fwd{host: hostA, headers: navHeaders})

	r := e.get(e.metrics.URL, "/metrics")
	if r.status != http.StatusOK || !strings.HasPrefix(r.header.Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics listener: status %d, content type %q", r.status, r.header.Get("Content-Type"))
	}
	for _, want := range []string{
		"# TYPE platform_forwardauth_decisions_total counter",
		"# TYPE platform_forwardauth_decision_duration_seconds histogram",
		"# TYPE platform_signing_key_loaded gauge",
		"# TYPE platform_db_pool_acquired_connections gauge",
		"# TYPE go_goroutines gauge",
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("exposition lacks %q", want)
		}
	}
	// Nothing else is served there.
	for _, path := range []string{"/forwardauth", "/readyz", jwks.Path} {
		if r := e.get(e.metrics.URL, path); r.status != http.StatusNotFound {
			t.Errorf("metrics listener %s = %d, want 404", path, r.status)
		}
	}

	for name, base := range map[string]string{"internal": e.internal.URL, "public": e.public.URL, "jwks": e.jwks.URL} {
		for _, path := range []string{"/metrics", "/metrics/", "/metrics?x=1"} {
			r := e.get(base, path)
			if r.status != http.StatusNotFound && r.status != http.StatusMovedPermanently {
				t.Errorf("%s %s = %d, want 404", name, path, r.status)
			}
			if strings.Contains(r.body, "platform_") || strings.Contains(r.body, "go_goroutines") {
				t.Errorf("%s %s leaked metrics", name, path)
			}
		}
	}
	// Only GET is routed.
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, e.metrics.URL+"/metrics", nil)
	if r := e.newBrowser().do(req); r.status != http.StatusMethodNotAllowed {
		t.Errorf("POST /metrics = %d, want 405", r.status)
	}
}

// The JWKS has a listener of its own, the only port app namespaces reach: it serves nothing else, and neither
// the internal listener nor the metrics one serves the JWKS.
func TestJWKSHasItsOwnListener(t *testing.T) {
	e := newEnv(t, envOptions{})

	r := e.get(e.jwks.URL, jwks.Path)
	if r.status != http.StatusOK || !strings.Contains(r.body, `"keys"`) {
		t.Fatalf("jwks listener: status %d", r.status)
	}
	if got := r.header.Get("Content-Type"); got != "application/jwk-set+json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := r.header.Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("Cache-Control = %q", got)
	}
	for _, path := range []string{"/forwardauth", "/healthz", "/readyz", "/metrics", "/", "/login"} {
		if r := e.get(e.jwks.URL, path); r.status != http.StatusNotFound {
			t.Errorf("jwks listener %s = %d, want 404", path, r.status)
		}
	}
	for name, base := range map[string]string{"internal": e.internal.URL, "metrics": e.metrics.URL} {
		if r := e.get(base, jwks.Path); r.status != http.StatusNotFound || strings.Contains(r.body, `"keys"`) {
			t.Errorf("%s %s = %d, want 404", name, jwks.Path, r.status)
		}
	}
	// The internal listener keeps ForwardAuth and the probes.
	for _, path := range []string{"/healthz", "/readyz"} {
		if r := e.get(e.internal.URL, path); r.status != http.StatusOK {
			t.Errorf("internal %s = %d, want 200", path, r.status)
		}
	}
	// Only GET is routed.
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, e.jwks.URL+jwks.Path, nil)
	if r := e.newBrowser().do(req); r.status != http.StatusMethodNotAllowed {
		t.Errorf("POST JWKS = %d, want 405", r.status)
	}
}

func TestMetricsCountDecisionsCachesLoginsAndLogouts(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()
	dec := func(decision, reason string) map[string]string {
		return map[string]string{"decision": decision, "reason": reason}
	}

	b.login(hostA, "/", userEmail)
	if got := e.metric("platform_logins_total", map[string]string{"outcome": "success"}); got != 1 {
		t.Errorf("successful logins = %v, want 1", got)
	}
	for _, want := range []struct{ decision, reason string }{{"login_redirect", "no_cookie"}, {"callback", "callback_ok"}} {
		if got := e.metric("platform_forwardauth_decisions_total", dec(want.decision, want.reason)); got != 1 {
			t.Errorf("%s/%s = %v, want 1", want.decision, want.reason, got)
		}
	}

	// A signed-in user without a grant is denied, then the grant arrives.
	b.forward(fwd{host: hostA, headers: navHeaders})
	if got := e.metric("platform_forwardauth_decisions_total", dec("deny", "no_grant")); got != 1 {
		t.Errorf("deny/no_grant = %v, want 1", got)
	}
	e.grant(userEmail)
	e.app.InvalidateAccess()
	decisionHits := func(result string) float64 {
		v, _ := e.sample("platform_forwardauth_cache_lookups_total", map[string]string{"cache": "decision", "result": result})
		return v
	}
	missBefore, hitBefore := decisionHits("miss"), decisionHits("hit")
	for range 3 {
		if r := b.forward(fwd{host: hostA, uri: "/api", headers: apiHeaders}); r.status != http.StatusOK {
			t.Fatalf("authorized request = %d", r.status)
		}
	}
	if got := e.metric("platform_forwardauth_decisions_total", dec("allow", "granted")); got != 3 {
		t.Errorf("allow/granted = %v, want 3", got)
	}
	if miss, hit := decisionHits("miss")-missBefore, decisionHits("hit")-hitBefore; miss != 1 || hit != 2 {
		t.Errorf("decision cache after 3 requests: %v misses and %v hits, want 1 and 2", miss, hit)
	}
	if got := e.metric("platform_forwardauth_decision_duration_seconds", map[string]string{"decision": "allow"}); got != 3 {
		t.Errorf("duration samples for allow = %v, want 3", got)
	}
	if got := e.metric("platform_lastseen_pending_hosts", map[string]string{}); got != 1 {
		t.Errorf("pending last_seen hosts = %v, want 1", got)
	}
	if err := e.app.LastSeen.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.metric("platform_lastseen_pending_hosts", map[string]string{}); got != 0 {
		t.Errorf("pending last_seen hosts after the flush = %v, want 0", got)
	}
	if got := e.metric("platform_lastseen_flush_errors_total", map[string]string{}); got != 0 {
		t.Errorf("flush errors = %v, want 0", got)
	}

	// Hosts without deployment: a valid name is cached negatively, an invalid one is refused before any lookup.
	anon := e.newBrowser()
	for range 2 {
		anon.forward(fwd{host: "ghost.apps.example.test", headers: navHeaders})
	}
	anon.forward(fwd{host: "not_a_host!", headers: navHeaders})
	if got := e.metric("platform_forwardauth_decisions_total", dec("deny", "unknown_host")); got != 3 {
		t.Errorf("deny/unknown_host = %v, want 3", got)
	}
	if got := e.metric("platform_forwardauth_cache_lookups_total", map[string]string{"cache": "negative_host", "result": "hit"}); got != 1 {
		t.Errorf("negative cache hits = %v, want 1", got)
	}
	if got := e.metric("platform_unknown_host_drops_total", map[string]string{"reason": "invalid"}); got != 1 {
		t.Errorf("invalid host drops = %v, want 1", got)
	}

	// Logouts: through the app host, then through the auth host.
	b.forward(fwd{host: hostA, method: http.MethodPost, uri: "/_platform/logout", headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
	if got := e.metric("platform_logouts_total", map[string]string{"via": "app"}); got != 1 {
		t.Errorf("app logouts = %v, want 1", got)
	}
	b2 := e.newBrowser()
	b2.login(hostB, "/", "other@example.test")
	if r := b2.auth(http.MethodPost, "/logout", nil, sameOrigin); r.status != http.StatusSeeOther {
		t.Fatalf("auth host logout = %d", r.status)
	}
	if got := e.metric("platform_logouts_total", map[string]string{"via": "auth_host"}); got != 1 {
		t.Errorf("auth host logouts = %v, want 1", got)
	}
	// A logout without a session revokes nothing and is not counted.
	e.newBrowser().auth(http.MethodPost, "/logout", nil, sameOrigin)
	if got := e.metric("platform_logouts_total", map[string]string{"via": "auth_host"}); got != 1 {
		t.Errorf("auth host logouts after a logout with no session = %v, want still 1", got)
	}

	// JWKS, per listener.
	e.get(e.jwks.URL, jwks.Path)
	e.get(e.public.URL, jwks.Path)
	e.get(e.public.URL, jwks.Path)
	for listener, want := range map[string]float64{"jwks": 1, "public": 2} {
		if got := e.metric("platform_jwks_requests_total", map[string]string{"listener": listener, "status_class": "2xx"}); got != want {
			t.Errorf("JWKS requests on %s = %v, want %v", listener, got, want)
		}
	}

	// State read at scrape time.
	if got := e.metric("platform_signing_key_loaded", map[string]string{}); got != 1 {
		t.Errorf("signing key loaded = %v, want 1", got)
	}
	if got := e.metric("platform_signing_key_kek_mismatch", map[string]string{}); got != 0 {
		t.Errorf("KEK mismatch = %v, want 0", got)
	}
	if age := e.metric("platform_signing_key_active_age_seconds", map[string]string{}); age < 0 || age > 60 {
		t.Errorf("active key age = %v, want a fresh key", age)
	}
	if got := e.metric("platform_db_pool_max_connections", map[string]string{}); got < 1 {
		t.Errorf("pool max connections = %v", got)
	}
	if got := e.metric("platform_db_pool_acquires_total", map[string]string{}); got < 1 {
		t.Errorf("pool acquires = %v after all this traffic", got)
	}
}

func TestMetricsReportSigningKeyStates(t *testing.T) {
	t.Run("no signer loaded", func(t *testing.T) {
		e := newEnv(t, envOptions{noReload: true})
		if got := e.metric("platform_signing_key_loaded", map[string]string{}); got != 0 {
			t.Errorf("loaded = %v, want 0", got)
		}
		if _, ok := e.sample("platform_signing_key_active_age_seconds", map[string]string{}); ok {
			t.Error("the age of a key that is not loaded was reported")
		}
	})
	t.Run("KEK mismatch", func(t *testing.T) {
		other, _, err := keys.GenerateKEK()
		if err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, envOptions{ringKEK: other})
		if got := e.metric("platform_signing_key_kek_mismatch", map[string]string{}); got != 1 {
			t.Errorf("KEK mismatch = %v, want 1", got)
		}
		if got := e.metric("platform_signing_key_loaded", map[string]string{}); got != 0 {
			t.Errorf("loaded = %v, want 0 while the KEK does not match", got)
		}
	})
}

func TestMetricsCountGoogleLoginOutcomes(t *testing.T) {
	fg := newFakeGoogle(t)
	e := newEnv(t, envOptions{google: fg})
	outcome := func(o string) float64 {
		v, _ := e.sample("platform_logins_total", map[string]string{"outcome": o})
		return v
	}

	b := e.newBrowser()
	e.googleLogin(b, hostA, "/")
	if got := outcome("success"); got != 1 {
		t.Errorf("success = %v, want 1", got)
	}

	f := e.beginGoogle(e.newBrowser(), hostA, "/")
	f.provider()
	f.callback(func(q url.Values) { q.Set("state", "x"+q.Get("state")) })
	if got := outcome("invalid_flow"); got != 1 {
		t.Errorf("invalid_flow = %v, want 1", got)
	}

	f = e.beginGoogle(e.newBrowser(), hostA, "/")
	f.provider()
	f.callback(func(q url.Values) { q.Set("error", "access_denied"); q.Del("code") })
	if got := outcome("provider_refused"); got != 1 {
		t.Errorf("provider_refused = %v, want 1", got)
	}

	fg.setAccount(account{Sub: "100000000000000000009", Email: "eve@example.test", Name: "Eve", HD: googleHD, EmailVerified: false})
	f = e.beginGoogle(e.newBrowser(), hostA, "/")
	f.provider()
	f.callback(nil)
	if got := outcome("rejected"); got != 1 {
		t.Errorf("rejected = %v, want 1", got)
	}
	if got := outcome("success"); got != 1 {
		t.Errorf("success changed to %v after failed logins", got)
	}
}

// Hostile input must not create series. Hosts, paths, methods and cookies are all supplied by whoever can reach
// the listener; the number of series after thousands of them must equal the number after one of each kind, and
// none of their values may appear in the exposition.
func TestMetricsCardinalityDoesNotGrowWithHostileInput(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()
	b.login(hostA, "/", userEmail)
	e.grant(userEmail)
	e.app.InvalidateAccess()

	hex := func(r *rand.Rand) string { return fmt.Sprintf("%016x", r.Uint64()) }
	r := rand.New(rand.NewPCG(7, 11)) //nolint:gosec // G404: reproducible hostile input, not a secret
	const sentinel = "zz-hostile-"
	// One request of each kind, by the same function the flood uses: this creates every series the flood can reach.
	flood := func(i int) {
		anon := e.newBrowser()
		switch i % 8 {
		case 0: // a valid but unregistered name
			anon.forward(fwd{host: sentinel + hex(r) + ".apps.example.test", uri: "/" + sentinel + hex(r), headers: navHeaders})
		case 1: // not a valid DNS name
			anon.forward(fwd{host: strings.ToUpper(sentinel) + "_" + hex(r) + "!", headers: apiHeaders})
		case 2: // too long
			anon.forward(fwd{host: strings.Repeat("a", 64) + hex(r) + ".example.test", headers: apiHeaders})
		case 3: // a registered host, random path and method, no cookie
			anon.forward(fwd{host: hostA, method: "M" + strings.ToUpper(hex(r)), uri: "/" + sentinel + hex(r) + "?q=" + hex(r), headers: apiHeaders})
		case 4: // a registered host with a garbage app cookie
			anon.forward(fwd{host: hostB, uri: "/" + hex(r), headers: map[string]string{
				"Accept": "text/html", "Sec-Fetch-Mode": "navigate", "Cookie": "__Host-Http-platform_app=" + sentinel + hex(r),
			}})
		case 5: // a random callback code
			anon.forward(fwd{host: hostC, uri: "/_platform/callback?code=" + sentinel + hex(r), headers: navHeaders})
		case 6: // random paths, methods and queries on every route of both listeners
			for _, base := range []string{e.internal.URL, e.public.URL, e.jwks.URL} {
				req, _ := http.NewRequestWithContext(t.Context(), "X"+strings.ToUpper(hex(r)), base+"/"+sentinel+hex(r)+"?"+sentinel+hex(r)+"=1", nil)
				req.Host = sentinel + hex(r) + ".example.test"
				anon.do(req)
			}
		case 7: // the authorized user, random path
			b.forward(fwd{host: hostA, uri: "/" + sentinel + hex(r), headers: apiHeaders})
		}
	}
	for i := range 8 {
		flood(i)
	}
	e.app.InvalidateAccess() // the sampling below must not depend on a cache that is warm only for the first pass
	before := e.ownSeries()

	const requests = 1600
	for i := range requests {
		flood(i)
	}
	if after := e.ownSeries(); after != before {
		t.Fatalf("series grew from %d to %d after %d hostile requests", before, after, requests)
	}
	body := e.get(e.metrics.URL, "/metrics").body
	if strings.Contains(strings.ToLower(body), sentinel) {
		t.Fatal("a value supplied by the client appears in the exposition")
	}
}
