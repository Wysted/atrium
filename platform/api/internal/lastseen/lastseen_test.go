package lastseen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeStore records every MarkSeen call.
type fakeStore struct {
	mu    sync.Mutex
	calls [][]Seen
	err   error
	skip  []string      // returned (once) as the hosts whose row was locked
	got   chan struct{} // receives one value per successful call when non-nil
}

func (f *fakeStore) MarkSeen(_ context.Context, seen []Seen) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, seen)
	if f.got != nil {
		select {
		case f.got <- struct{}{}:
		default:
		}
	}
	skipped := f.skip
	f.skip = nil
	return skipped, nil
}

func (f *fakeStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeStore) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newRecorder(t *testing.T, store Store, clk *clock, interval time.Duration) *Recorder {
	t.Helper()
	r, err := New(Config{Store: store, Interval: interval, Now: clk.now, Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNewRequiresAStore(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); err == nil {
		t.Fatal("New accepted a missing store")
	}
}

func TestTouchesAreAggregatedPerHostIntoOneWrite(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	clk := &clock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	r := newRecorder(t, store, clk, 0)

	for range 1000 {
		r.Touch("a.example.test")
		clk.advance(time.Millisecond)
	}
	r.Touch("b.example.test")
	if store.callCount() != 0 {
		t.Fatal("a request wrote to the store")
	}
	if got := r.Pending(); got != 2 {
		t.Fatalf("pending = %d, want 2 hosts for 1001 requests", got)
	}

	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.callCount() != 1 || len(store.calls[0]) != 2 {
		t.Fatalf("calls = %v, want one statement with two hosts", store.calls)
	}
	byHost := map[string]time.Time{}
	for _, s := range store.calls[0] {
		byHost[s.Host] = s.At
	}
	wantA := time.Date(2026, 10, 16, 12, 0, 0, 999*int(time.Millisecond), time.UTC)
	if !byHost["a.example.test"].Equal(wantA) {
		t.Fatalf("a was seen at %s, want the latest touch %s", byHost["a.example.test"], wantA)
	}

	// Nothing pending: the next tick writes nothing.
	if err := r.Flush(context.Background()); err != nil || store.callCount() != 1 {
		t.Fatalf("empty flush: err %v, calls %d", err, store.callCount())
	}
}

func TestFailedFlushIsRetriedWithoutLosingNewerObservations(t *testing.T) {
	t.Parallel()
	store := &fakeStore{err: errors.New("database down")}
	clk := &clock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	r := newRecorder(t, store, clk, 0)

	r.Touch("a.example.test")
	if err := r.Flush(context.Background()); err == nil {
		t.Fatal("flush hid the store error")
	}
	if r.Pending() != 1 {
		t.Fatalf("the failed batch was dropped: pending %d", r.Pending())
	}

	clk.advance(time.Minute)
	r.Touch("a.example.test") // newer than the restored one
	newest := clk.now()
	clk.advance(time.Minute)
	r.Touch("b.example.test")
	store.setErr(nil)
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := map[string]time.Time{}
	for _, s := range store.calls[0] {
		got[s.Host] = s.At
	}
	if len(got) != 2 || !got["a.example.test"].Equal(newest) {
		t.Fatalf("retry wrote %v, want a at its newest time and b", got)
	}
}

// A row locked by someone else is not lost: the host stays pending and is written by the next tick, with its time
// unless a newer request touched it meanwhile.
func TestSkippedHostsAreRetried(t *testing.T) {
	t.Parallel()
	store := &fakeStore{skip: []string{"a.example.test"}}
	clk := &clock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	r := newRecorder(t, store, clk, 0)
	r.Touch("a.example.test")
	first := clk.now()
	clk.advance(time.Second)
	r.Touch("b.example.test")
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.snapshot(); len(got) != 1 || !got["a.example.test"].Equal(first) {
		t.Fatalf("pending after the flush = %v, want only the skipped host", got)
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.callCount() != 2 || len(store.calls[1]) != 1 || store.calls[1][0].Host != "a.example.test" || r.Pending() != 0 {
		t.Fatalf("calls = %v, pending %d", store.calls, r.Pending())
	}

	// A newer touch beats the restored time.
	store.skip = []string{"a.example.test"}
	r.Touch("a.example.test")
	clk.advance(time.Second)
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Second)
	r.Touch("a.example.test")
	newest := clk.now()
	if got := r.snapshot()["a.example.test"]; !got.Equal(newest) {
		t.Fatalf("pending = %s, want the newest touch %s", got, newest)
	}
}

func TestRestoreKeepsNewerObservation(t *testing.T) {
	t.Parallel()
	clk := &clock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	r := newRecorder(t, &fakeStore{}, clk, 0)
	old, newer := clk.now(), clk.now().Add(time.Minute)
	r.pending["a.example.test"] = newer
	r.restore(map[string]time.Time{"a.example.test": old})
	if got := r.snapshot()["a.example.test"]; !got.Equal(newer) {
		t.Fatalf("restore overwrote a newer observation with %s", got)
	}
}

func TestPendingIsBounded(t *testing.T) {
	t.Parallel()
	r := newRecorder(t, &fakeStore{}, &clock{t: time.Now()}, 0)
	for i := range MaxPending + 50 {
		r.Touch(fmt.Sprintf("h%d.example.test", i))
	}
	if got := r.Pending(); got != MaxPending {
		t.Fatalf("pending = %d, want the bound %d", got, MaxPending)
	}
	r.Touch("h0.example.test") // a known host still updates
	if got := r.Pending(); got != MaxPending {
		t.Fatalf("pending = %d after touching a known host", got)
	}
}

func TestRunFlushesOncePerTick(t *testing.T) {
	t.Parallel()
	store := &fakeStore{got: make(chan struct{}, 8)}
	r := newRecorder(t, store, &clock{t: time.Now()}, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	for range 100 {
		r.Touch("a.example.test")
		r.Touch("b.example.test")
	}
	select {
	case <-store.got:
	case <-time.After(5 * time.Second):
		t.Fatal("no flush within 5s")
	}
	cancel()
	<-done
	store.mu.Lock()
	defer store.mu.Unlock()
	for i, c := range store.calls {
		if len(c) > 2 {
			t.Fatalf("statement %d carries %d rows for 2 hosts", i, len(c))
		}
	}
}

func TestShutdownFlushesWhatIsPending(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	r := newRecorder(t, store, &clock{t: time.Now()}, time.Hour) // the ticker never fires
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	r.Touch("a.example.test")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if store.callCount() != 1 || store.calls[0][0].Host != "a.example.test" {
		t.Fatalf("shutdown flush calls = %v", store.calls)
	}
}

func TestShutdownFlushFailureIsNotFatal(t *testing.T) {
	t.Parallel()
	store := &fakeStore{err: errors.New("database gone")}
	r := newRecorder(t, store, &clock{t: time.Now()}, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	r.Touch("a.example.test")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run blocked on a failing final flush")
	}
}

// Many requests touch while flushes run: every host ends up written at least once and nothing races.
func TestConcurrentTouchAndFlush(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	r := newRecorder(t, store, &clock{t: time.Now()}, 0)
	const (
		workers = 16
		hosts   = 40
	)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range 500 {
				r.Touch(fmt.Sprintf("h%d.example.test", (w+i)%hosts))
			}
		})
	}
	stop := make(chan struct{})
	var flusher sync.WaitGroup
	flusher.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = r.Flush(context.Background())
			}
		}
	})
	wg.Wait()
	close(stop)
	flusher.Wait()
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	written := map[string]bool{}
	for _, c := range store.calls {
		for _, s := range c {
			written[s.Host] = true
		}
	}
	if len(written) != hosts {
		t.Fatalf("%d hosts written, want %d", len(written), hosts)
	}
}
