package forwardauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/access"
	"github.com/Wysted/atrium/platform/api/internal/assertion"
	"github.com/Wysted/atrium/platform/api/internal/audit"
)

// Stubs: an unknown host never reaches any of them but the app directory.
type (
	noApps     struct{}
	stubSess   struct{ Sessions }
	stubEval   struct{}
	stubProf   struct{}
	stubIssuer struct{}
	stubAudit  struct{ audit.Recorder }
)

func (noApps) AppForHost(context.Context, string) (string, bool, error) { return "", false, nil }
func (stubEval) Evaluate(context.Context, access.Request) (access.Decision, error) {
	return access.Decision{}, nil
}
func (stubProf) Profile(context.Context, string) (access.Profile, error) {
	return access.Profile{}, nil
}
func (stubIssuer) Issue(assertion.Request) (assertion.Issued, error) { return assertion.Issued{}, nil }

var _ Sessions = stubSess{}

// recordingDenials collects the denials; with block set, every insert hangs until the context ends (a stuck DB).
type recordingDenials struct {
	mu    sync.Mutex
	rows  []access.Denial
	block bool
}

func (d *recordingDenials) RecordDenial(ctx context.Context, row access.Denial) error {
	if d.block {
		<-ctx.Done()
		return ctx.Err()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rows = append(d.rows, row)
	return nil
}

func (d *recordingDenials) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.rows)
}

type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fixedClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newFloodHandler(t *testing.T, denials Denials, clk *fixedClock) *Handler {
	t.Helper()
	h, err := New(Config{
		AuthHost: "auth.apps.example.test", Apps: noApps{}, Sessions: stubSess{}, Evaluator: stubEval{}, Profiles: stubProf{},
		Denials: denials, Issuer: stubIssuer{}, Audit: stubAudit{}, Tokens: noopTokens{}, Now: clk.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func forwardHost(h http.Handler, host string) *httptest.ResponseRecorder {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/forwardauth", nil)
	r.Header.Set("X-Forwarded-Host", host)
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func runWriter(t *testing.T, h *Handler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { h.Run(ctx) })
	t.Cleanup(func() { cancel(); wg.Wait() })
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Ten thousand distinct valid hosts cannot write more rows than the budget allows, whatever the writer does.
func TestUnknownHostFloodStaysInsideTheBudget(t *testing.T) {
	t.Parallel()
	clk := &fixedClock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	den := &recordingDenials{}
	h := newFloodHandler(t, den, clk)
	runWriter(t, h)

	for i := range 10_000 {
		if w := forwardHost(h, fmt.Sprintf("flood%d.apps.example.test", i)); w.Code != http.StatusForbidden {
			t.Fatalf("host %d: status %d", i, w.Code)
		}
	}
	waitUntil(t, "the burst to be written", func() bool { return den.count() == UnknownHostBurst })
	time.Sleep(50 * time.Millisecond) // anything over the budget would land by now
	if got := den.count(); got != UnknownHostBurst {
		t.Fatalf("%d rows for 10000 hosts, want the burst of %d", got, UnknownHostBurst)
	}
	if d := h.UnknownHostDrops(); d.Budget < 10_000-UnknownHostBurst-UnknownHostQueueSize || d.Invalid != 0 {
		t.Fatalf("drops = %+v", d)
	}

	// The bucket refills with time: a minute buys the per-minute budget, not more.
	clk.advance(time.Minute)
	for i := range 1000 {
		forwardHost(h, fmt.Sprintf("second%d.apps.example.test", i))
	}
	waitUntil(t, "the refill to be written", func() bool { return den.count() == UnknownHostBurst+UnknownHostBudgetPerMinute })
	time.Sleep(50 * time.Millisecond)
	if got := den.count(); got != UnknownHostBurst+UnknownHostBudgetPerMinute {
		t.Fatalf("%d rows after a minute, want %d", got, UnknownHostBurst+UnknownHostBudgetPerMinute)
	}
}

func TestUnknownHostInvalidHostsAreNeverRecorded(t *testing.T) {
	t.Parallel()
	clk := &fixedClock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	den := &recordingDenials{}
	h := newFloodHandler(t, den, clk)
	runWriter(t, h)

	label := strings.Repeat("a", 64)
	bad := []string{
		"", "a_b.example.test", "-a.example.test", "a..example.test", "example.test.", "*.example.test",
		"example.test:8443", "10.0.0.1", "a b.example.test", label + ".example.test", strings.Repeat("a.", 130) + "test", "bücher.example.test",
	}
	for _, host := range bad {
		if w := forwardHost(h, host); w.Code != http.StatusForbidden {
			t.Fatalf("%q: status %d", host, w.Code)
		}
	}
	forwardHost(h, "good.example.test")
	waitUntil(t, "the valid host", func() bool { return den.count() == 1 })
	if got := den.count(); got != 1 {
		t.Fatalf("%d rows, want only the valid host", got)
	}
	if d := h.UnknownHostDrops(); d.Invalid != int64(len(bad)) {
		t.Fatalf("invalid drops = %d, want %d", d.Invalid, len(bad))
	}
}

func TestUnknownHostOncePerWindowAndPerHost(t *testing.T) {
	t.Parallel()
	clk := &fixedClock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	den := &recordingDenials{}
	h := newFloodHandler(t, den, clk)
	runWriter(t, h)

	for range 50 {
		forwardHost(h, "a.example.test")
	}
	waitUntil(t, "the first row", func() bool { return den.count() == 1 })
	clk.advance(UnknownHostRecordTTL - time.Second)
	forwardHost(h, "a.example.test")
	clk.advance(2 * time.Second)
	forwardHost(h, "a.example.test") // the marker expired: a second row
	waitUntil(t, "the second row", func() bool { return den.count() == 2 })
	time.Sleep(30 * time.Millisecond)
	if got := den.count(); got != 2 {
		t.Fatalf("%d rows, want 2", got)
	}
}

// The response never waits for the writer: with a database that hangs, and no writer at all, requests return at
// once and the queue simply fills and drops.
func TestUnknownHostNeverBlocksTheResponse(t *testing.T) {
	t.Parallel()
	clk := &fixedClock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	for name, withWriter := range map[string]bool{"stuck database": true, "no writer": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newFloodHandler(t, &recordingDenials{block: true}, clk)
			if withWriter {
				runWriter(t, h)
			}
			start := time.Now()
			for i := range 5000 {
				if w := forwardHost(h, fmt.Sprintf("h%d.apps.example.test", i)); w.Code != http.StatusForbidden {
					t.Fatalf("status %d", w.Code)
				}
				clk.advance(time.Second) // refill the bucket so the queue is the limit
			}
			if elapsed := time.Since(start); elapsed > 4*time.Second {
				t.Fatalf("5000 requests took %s: the handler waits for the recorder", elapsed)
			}
			if d := h.UnknownHostDrops(); d.Queue == 0 && !withWriter {
				t.Fatalf("drops = %+v, want the full queue to drop", d)
			}
		})
	}
}

func TestTokenBucket(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	b := newTokenBucket(30, 30, now)
	taken := 0
	for b.take(now) {
		taken++
	}
	if taken != 30 {
		t.Fatalf("burst = %d, want 30", taken)
	}
	if b.take(now.Add(time.Second)) {
		t.Fatal("one second must not refill a whole token at 30 per minute")
	}
	if !b.take(now.Add(2 * time.Second)) {
		t.Fatal("two seconds buy one token")
	}
	later := now.Add(time.Hour)
	taken = 0
	for b.take(later) {
		taken++
	}
	if taken != 30 {
		t.Fatalf("an idle hour refilled %d tokens, want the burst of 30", taken)
	}
}

// A full cache does not walk the map on every put.
func TestFullCachePutIsCheap(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	c := newBoundedTTLCache[int, int](time.Hour, 1000, func() time.Time { return now })
	for i := range 1000 {
		if !c.put(i, i, 0) {
			t.Fatalf("put %d refused before the bound", i)
		}
	}
	start := time.Now()
	for i := range 200_000 {
		if c.put(10_000+i, i, 0) {
			t.Fatal("a full cache stored an entry")
		}
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("200000 puts into a full cache took %s: it sweeps on every put", elapsed)
	}
	// Expired entries are swept once the interval has passed.
	now = now.Add(2 * time.Hour)
	if !c.put(-1, 1, 0) {
		t.Fatal("expired entries were never swept")
	}
}

// mapApps is an app directory that counts its lookups.
type mapApps struct {
	mu    sync.Mutex
	hosts map[string]string
	calls int
}

func (m *mapApps) AppForHost(_ context.Context, host string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	id, ok := m.hosts[host]
	return id, ok, nil
}

func (m *mapApps) lookups() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func newAppsHandler(t *testing.T, apps AppDirectory, clk *fixedClock) *Handler {
	t.Helper()
	h, err := New(Config{
		AuthHost: "auth.apps.example.test", Apps: apps, Sessions: stubSess{}, Evaluator: stubEval{}, Profiles: stubProf{},
		Denials: &recordingDenials{}, Issuer: stubIssuer{}, Audit: stubAudit{}, Tokens: noopTokens{}, Now: clk.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// A host that cannot be a deployment host is refused before any lookup: no SELECT, no cache slot.
func TestInvalidHostIsRefusedWithoutLookup(t *testing.T) {
	t.Parallel()
	clk := &fixedClock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	apps := &mapApps{hosts: map[string]string{}}
	h := newAppsHandler(t, apps, clk)
	for _, host := range []string{"", "a_b.example.test", "example.test:8443", "10.0.0.1", strings.Repeat("a", 64) + ".test"} {
		if w := forwardHost(h, host); w.Code != http.StatusForbidden {
			t.Fatalf("%q: status %d", host, w.Code)
		}
	}
	if apps.lookups() != 0 || len(h.hosts.m) != 0 || len(h.noHosts.m) != 0 {
		t.Fatalf("lookups %d, cached %d+%d: an invalid host reached the database or a cache", apps.lookups(), len(h.hosts.m), len(h.noHosts.m))
	}
	if d := h.UnknownHostDrops(); d.Invalid != 5 {
		t.Fatalf("invalid drops = %d", d.Invalid)
	}
}

// Random valid names fill the small negative cache, never the cache of known hosts.
func TestFloodOfUnknownHostsDoesNotEvictKnownOnes(t *testing.T) {
	t.Parallel()
	clk := &fixedClock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	apps := &mapApps{hosts: map[string]string{"app.apps.example.test": "app_x"}}
	h := newAppsHandler(t, apps, clk)

	for i := range 3 * MaxNegativeHostEntries {
		forwardHost(h, fmt.Sprintf("rnd%d.apps.example.test", i))
	}
	if len(h.noHosts.m) > MaxNegativeHostEntries || len(h.hosts.m) != 0 {
		t.Fatalf("negative cache %d (cap %d), known-host cache %d", len(h.noHosts.m), MaxNegativeHostEntries, len(h.hosts.m))
	}
	before := apps.lookups()
	for range 5 {
		if w := forwardHost(h, "app.apps.example.test"); w.Code != http.StatusFound {
			t.Fatalf("legitimate host: status %d", w.Code)
		}
	}
	if got := apps.lookups() - before; got != 1 {
		t.Fatalf("%d lookups for 5 requests to a known host after the flood, want 1 (then cached)", got)
	}
	if _, ok, _ := h.hosts.get("app.apps.example.test"); !ok {
		t.Fatal("the known host is not cached")
	}
}

// Negatives are cached for the TTL and dropped by Invalidate (access_changed): a new deployment is picked up.
func TestNegativeHostIsInvalidated(t *testing.T) {
	t.Parallel()
	clk := &fixedClock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	apps := &mapApps{hosts: map[string]string{}}
	h := newAppsHandler(t, apps, clk)
	const host = "new.apps.example.test"

	if w := forwardHost(h, host); w.Code != http.StatusForbidden {
		t.Fatalf("status %d", w.Code)
	}
	forwardHost(h, host)
	if apps.lookups() != 1 {
		t.Fatalf("%d lookups, the negative was not cached", apps.lookups())
	}
	apps.mu.Lock()
	apps.hosts[host] = "app_new" // the deployment is created...
	apps.mu.Unlock()
	h.Invalidate() // ...and NOTIFY access_changed arrives
	if w := forwardHost(h, host); w.Code != http.StatusFound {
		t.Fatalf("after the invalidation: status %d, want the login redirect", w.Code)
	}
	// Without a NOTIFY, the TTL alone also drops a negative.
	apps.mu.Lock()
	apps.hosts["later.apps.example.test"] = "app_later"
	apps.mu.Unlock()
	forwardHost(h, "later.apps.example.test") // found right away: not negative
	clk.advance(DefaultCacheTTL + time.Second)
	if _, ok, _ := h.noHosts.get(host); ok {
		t.Fatal("a negative outlived its TTL")
	}
}

// A host is not silenced when it could not be queued, and a failed attempt does not spend budget.
func TestOfferKeepsMarkersAndTokensHonest(t *testing.T) {
	t.Parallel()
	clk := &fixedClock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	r := newUnknownHostRecorder(clk.now)

	for i := range UnknownHostQueueSize {
		r.offer(fmt.Sprintf("q%d.example.test", i))
		clk.advance(2 * time.Second) // one token per call at 30 per minute
	}
	r.offer("late.example.test") // the queue is full
	if d := r.drops(); d.Queue != 1 {
		t.Fatalf("drops = %+v", d)
	}
	if _, marked, _ := r.markers.get("late.example.test"); marked {
		t.Fatal("a host that was not queued stays silenced")
	}
	<-r.queue
	r.offer("late.example.test")
	if len(r.queue) != UnknownHostQueueSize {
		t.Fatalf("queue = %d: the host was not queued once there was room", len(r.queue))
	}

	// Marker cache full: nothing queued and no token spent.
	r2 := newUnknownHostRecorder(clk.now)
	r2.markers = newBoundedTTLCache[string, struct{}](UnknownHostRecordTTL, 1, clk.now)
	r2.offer("one.example.test")
	for range 5 {
		r2.offer("two.example.test")
	}
	if d := r2.drops(); d.Marker != 5 {
		t.Fatalf("drops = %+v", d)
	}
	taken := 0
	for r2.budget.take(clk.now()) {
		taken++
	}
	if taken != UnknownHostBurst-1 {
		t.Fatalf("%d tokens left, want %d: failed attempts spent budget", taken, UnknownHostBurst-1)
	}
}
