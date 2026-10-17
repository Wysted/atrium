package tokenuse

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeStore struct {
	mu    sync.Mutex
	calls [][]Used
	err   error
}

func (f *fakeStore) MarkUsed(_ context.Context, used []Used) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, used)
	return nil
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

func newRecorder(t *testing.T, store Store, now *time.Time) *Recorder {
	t.Helper()
	r, err := New(Config{Store: store, Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNewRequiresAStore(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New without a store succeeded")
	}
}

// Many requests with the same token between two flushes are one write, with the latest time.
func TestTouchesOfOneTokenAreOneWrite(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{}
	r := newRecorder(t, store, &now)
	for range 50 {
		r.Touch("t1")
		now = now.Add(time.Second)
	}
	r.Touch("t2")
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.callCount() != 1 || len(store.calls[0]) != 2 {
		t.Fatalf("calls = %v, want one statement with two tokens", store.calls)
	}
	for _, u := range store.calls[0] {
		if u.ID == "t1" && !u.At.Equal(now.Add(-time.Second)) {
			t.Errorf("t1 written with %v, want its latest use", u.At)
		}
	}
	if err := r.Flush(t.Context()); err != nil || store.callCount() != 1 {
		t.Fatalf("empty flush hit the store (calls %d, err %v)", store.callCount(), err)
	}
}

func TestFailedFlushKeepsTheBatchAndNewerWins(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{err: errors.New("db down")}
	r := newRecorder(t, store, &now)
	r.Touch("t1")
	old := now
	if err := r.Flush(t.Context()); err == nil {
		t.Fatal("flush error swallowed")
	}
	now = now.Add(time.Minute)
	r.Touch("t1")
	store.setErr(nil)
	if err := r.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := store.calls[0]; len(got) != 1 || got[0].At.Equal(old) {
		t.Fatalf("written %v, want the newer observation", got)
	}
}

// Beyond MaxPending new tokens are dropped and counted; known ones still update.
func TestPendingIsBounded(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	r := newRecorder(t, &fakeStore{}, &now)
	for i := range MaxPending + 5 {
		r.Touch(fmt.Sprintf("t%d", i))
	}
	if r.Pending() != MaxPending || r.Dropped() != 5 {
		t.Fatalf("pending %d dropped %d, want %d and 5", r.Pending(), r.Dropped(), MaxPending)
	}
	now = now.Add(time.Second)
	r.Touch("t0")
	if r.Dropped() != 5 || !r.snapshot()["t0"].Equal(now) {
		t.Fatal("a known token was dropped or not refreshed at the bound")
	}
}

func TestRunFlushesOnTickAndAtShutdown(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{}
	r, err := New(Config{Store: store, Interval: 10 * time.Millisecond, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	r.Touch("t1")
	deadline := time.Now().Add(5 * time.Second)
	for store.callCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if store.callCount() == 0 {
		t.Fatal("no flush on tick")
	}
	r.Touch("t2")
	cancel()
	<-done
	if r.Pending() != 0 {
		t.Fatalf("pending %d after shutdown, want the final flush to write it", r.Pending())
	}
}
