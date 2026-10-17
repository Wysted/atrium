package keys_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/keys"
)

// flakyStore wraps a Store to fail LoadAll on demand and to hold the first LoadAll after its read.
type flakyStore struct {
	keys.Store
	fail atomic.Bool

	holdFirst bool          // the first LoadAll returns its (already read) records only after release
	calls     atomic.Int64  //
	read      chan struct{} // closed when the held LoadAll has read the database
	release   chan struct{}
}

func (s *flakyStore) LoadAll(ctx context.Context) ([]keys.Record, error) {
	if s.fail.Load() {
		return nil, errors.New("database unreachable")
	}
	recs, err := s.Store.LoadAll(ctx)
	if s.holdFirst && s.calls.Add(1) == 1 {
		close(s.read)
		<-s.release
	}
	return recs, err
}

// L4 (b): N consecutive failed reloads expire the signer (fail closed) until a reload succeeds.
func TestKeyringSignerExpiresAfterConsecutiveReloadFailures(t *testing.T) {
	r := newRig(t)
	r.tick(t, 0)
	store := &flakyStore{Store: r.store}
	ring, err := keys.NewKeyring(keys.KeyringConfig{Store: store, KEK: r.kek})
	if err != nil {
		t.Fatal(err)
	}
	ctx := r.ctx(t)
	if err := ring.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	failReloads := func(n int) {
		t.Helper()
		store.fail.Store(true)
		for range n {
			if err := ring.Reload(ctx); err == nil {
				t.Fatal("Reload succeeded against an unreachable database")
			}
		}
		store.fail.Store(false)
	}

	failReloads(keys.MaxConsecutiveReloadFailures - 1)
	if _, err := ring.Signer(); err != nil {
		t.Fatalf("signer dropped after %d failures: %v", keys.MaxConsecutiveReloadFailures-1, err)
	}
	// A success resets the count: another N-1 failures still keep the signer.
	if err := ring.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	failReloads(keys.MaxConsecutiveReloadFailures - 1)
	if _, err := ring.Signer(); err != nil {
		t.Fatalf("a successful reload did not reset the failure count: %v", err)
	}

	failReloads(1) // the Nth consecutive failure
	if _, err := ring.Signer(); !errors.Is(err, keys.ErrNoActiveKey) {
		t.Fatalf("Signer() err = %v, want ErrNoActiveKey after %d consecutive failures", err, keys.MaxConsecutiveReloadFailures)
	}
	if len(ring.PublicKeys()) == 0 {
		t.Fatal("the published set must survive a database outage")
	}

	if err := ring.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ring.Signer(); err != nil {
		t.Fatalf("signer not restored by a successful reload: %v", err)
	}
}

// L4 (a): a slow Reload that read the database before a revocation must not overwrite the snapshot of a
// Reload that started later and saw the revocation.
func TestKeyringConcurrentReloadsKeepTheNewestSnapshot(t *testing.T) {
	r := newRig(t)
	r.tick(t, 0)
	old := r.activeKID(t)
	store := &flakyStore{Store: r.store, holdFirst: true, read: make(chan struct{}), release: make(chan struct{})}
	ring, err := keys.NewKeyring(keys.KeyringConfig{Store: store, KEK: r.kek})
	if err != nil {
		t.Fatal(err)
	}
	ctx := r.ctx(t)

	var wg sync.WaitGroup
	reload := func() {
		wg.Go(func() {
			if err := ring.Reload(ctx); err != nil {
				t.Errorf("Reload: %v", err)
			}
		})
	}
	reload() // A: reads the database (old key active), then stalls before storing its snapshot
	<-store.read
	if err := r.rotator.Revoke(ctx, old); err != nil {
		t.Fatal(err)
	}
	reload()                          // B: starts after the revocation
	time.Sleep(50 * time.Millisecond) // let B reach (or finish, without the fix) its read
	close(store.release)
	wg.Wait()

	s, err := ring.Signer()
	if err != nil {
		t.Fatal(err)
	}
	if s.KID == old {
		t.Fatal("an older reload overwrote the newer snapshot: the revoked key is signing again")
	}
	for _, p := range ring.PublicKeys() {
		if p.KID == old {
			t.Fatal("the revoked key is published again")
		}
	}
}

// subStore replaces the LISTEN loop of a Store with one that takes a while to wind down.
type subStore struct {
	keys.Store
	started  chan struct{}
	finished atomic.Bool
}

func (s *subStore) Subscribe(ctx context.Context, _ func()) error {
	close(s.started)
	<-ctx.Done()
	time.Sleep(150 * time.Millisecond) // closing the LISTEN connection
	s.finished.Store(true)
	return nil
}

// Run owns its subscription goroutine: when Run returns, the LISTEN connection is already closed, so the
// caller can close the pool right after.
func TestKeyringRunWaitsForTheSubscription(t *testing.T) {
	r := newRig(t)
	r.tick(t, 0)
	store := &subStore{Store: r.store, started: make(chan struct{})}
	ring, err := keys.NewKeyring(keys.KeyringConfig{Store: store, KEK: r.kek})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { ring.Run(ctx); close(done) }()
	<-store.started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if !store.finished.Load() {
		t.Fatal("Run returned while the subscription goroutine was still running")
	}
}
