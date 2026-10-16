package keys

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultRefreshInterval is the safety-net reload period; NOTIFY normally triggers reloads sooner.
	DefaultRefreshInterval = 30 * time.Second
	// subscribeRetryDelay spaces LISTEN reconnect attempts.
	subscribeRetryDelay = 5 * time.Second
	// MaxConsecutiveReloadFailures is how many database reads in a row may fail before the signer expires
	// (about 90 s at DefaultRefreshInterval). A replica cut off from the database cannot learn that its
	// active key was revoked, so after this many it stops signing until a reload succeeds.
	MaxConsecutiveReloadFailures = 3
)

// KeyringConfig wires a Keyring. RefreshInterval and Logger are optional.
type KeyringConfig struct {
	Store           Store
	KEK             *KEK
	RefreshInterval time.Duration
	Logger          *slog.Logger
}

// snapshot is the immutable state served to readers.
type snapshot struct {
	signer *Signer
	// activeSince is when the key of signer became active (the creation time if the row has no activation time).
	activeSince time.Time
	published   []PublicKey
}

// Keyring is the read side used by every replica: it caches the active signer and the published
// public key set, refreshing from the database periodically and on NOTIFY signing_keys_changed.
type Keyring struct {
	store   Store
	kek     *KEK
	refresh time.Duration
	log     *slog.Logger
	snap    atomic.Pointer[snapshot]
	// mismatch is set while the active/next keys in the database were sealed under another KEK than this replica's.
	mismatch atomic.Bool

	reloadMu sync.Mutex // serializes Reload so an older read can never overwrite a newer snapshot
	failures int        // consecutive failed database reads; guarded by reloadMu
}

// NewKeyring validates cfg and applies defaults. Call Reload (or Run) before serving.
func NewKeyring(cfg KeyringConfig) (*Keyring, error) {
	if cfg.Store == nil || cfg.KEK == nil {
		return nil, errors.New("keyring: store and KEK are required")
	}
	k := &Keyring{store: cfg.Store, kek: cfg.KEK, refresh: cfg.RefreshInterval, log: cfg.Logger}
	if k.refresh <= 0 {
		k.refresh = DefaultRefreshInterval
	}
	if k.log == nil {
		k.log = slog.Default()
	}
	return k, nil
}

// Signer returns the active signing key, or ErrNoActiveKey.
func (k *Keyring) Signer() (Signer, error) {
	s := k.snap.Load()
	if s == nil || s.signer == nil {
		return Signer{}, ErrNoActiveKey
	}
	return *s.signer, nil
}

// ActiveSince returns when the loaded signing key became active, and false while no signer is loaded. It lets
// metrics report the age of the key without touching the key itself.
func (k *Keyring) ActiveSince() (time.Time, bool) {
	s := k.snap.Load()
	if s == nil || s.signer == nil {
		return time.Time{}, false
	}
	return s.activeSince, true
}

// KEKMismatch reports whether the last successful reload found active or next keys sealed under a KEK
// other than this replica's. While true the replica neither signs nor rotates; /readyz must fail.
func (k *Keyring) KEKMismatch() bool { return k.mismatch.Load() }

// PublicKeys returns the published set (active, next, retired), never private material.
func (k *Keyring) PublicKeys() []PublicKey {
	s := k.snap.Load()
	if s == nil {
		return nil
	}
	return slices.Clone(s.published)
}

// Reload rebuilds the snapshot from the database. If the database cannot be read, the previous snapshot
// stays in place, but after MaxConsecutiveReloadFailures consecutive failures the signer is dropped (the
// published set is kept) until a reload succeeds. If the database's active key cannot be loaded (wrong
// KEK, corrupt ciphertext), the new snapshot publishes what the database publishes but has no signer:
// Signer fails closed instead of signing with a key the database no longer marks active (it may have
// been revoked), and the error is returned.
//
// Concurrent calls are serialized, and each reads the database after acquiring the lock, so the snapshot
// stored last always comes from the most recent read.
func (k *Keyring) Reload(ctx context.Context) error {
	k.reloadMu.Lock()
	defer k.reloadMu.Unlock()

	recs, err := k.store.LoadAll(ctx)
	if err != nil {
		k.failures++
		if prev := k.snap.Load(); k.failures >= MaxConsecutiveReloadFailures && prev != nil && prev.signer != nil {
			k.log.Error("keyring cannot reach the database: signing disabled on this replica until a reload succeeds",
				"consecutive_failures", k.failures)
			k.snap.Store(&snapshot{published: prev.published})
		}
		return fmt.Errorf("load signing keys: %w", err)
	}
	k.failures = 0
	prev := k.snap.Load()
	next := &snapshot{}
	var unsealErr error
	kekErr := checkKEK(k.kek, recs)
	k.mismatch.Store(kekErr != nil)
	if kekErr != nil {
		k.log.Error("this replica was started with the wrong KEK: signing and rotation disabled, database untouched",
			"replica_kek_id", k.kek.ID(), "error", kekErr)
	}
	for _, rec := range recs {
		if !rec.State.published() {
			continue
		}
		if rec.JWK.Thumbprint() != rec.KID {
			k.log.Error("signing key skipped: public JWK does not match kid", "kid", rec.KID)
			continue
		}
		next.published = append(next.published, PublicKey{KID: rec.KID, State: rec.State, JWK: rec.JWK})
		if rec.State != StateActive || kekErr != nil {
			continue
		}
		since := rec.CreatedAt
		if rec.ActivatedAt != nil {
			since = *rec.ActivatedAt
		}
		if prev != nil && prev.signer != nil && prev.signer.KID == rec.KID {
			next.signer, next.activeSince = prev.signer, since // already unsealed
			continue
		}
		signer, err := unseal(k.kek, rec)
		if err != nil {
			unsealErr = fmt.Errorf("unseal active key: %w", err)
			k.log.Error("active signing key cannot be loaded: signing disabled on this replica until it can",
				"kid", rec.KID, "error", err)
			continue
		}
		next.signer, next.activeSince = &signer, since
	}
	slices.SortFunc(next.published, func(a, b PublicKey) int {
		return cmp.Or(cmp.Compare(stateOrder(a.State), stateOrder(b.State)), cmp.Compare(a.KID, b.KID))
	})
	k.snap.Store(next)
	if kekErr != nil {
		return kekErr
	}
	return unsealErr
}

func stateOrder(s State) int {
	switch s {
	case StateActive:
		return 0
	case StateNext:
		return 1
	default:
		return 2
	}
}

// Run reloads on a timer and on every signing_keys NOTIFY until ctx ends.
func (k *Keyring) Run(ctx context.Context) {
	changed := make(chan struct{}, 1)
	notify := func() {
		select {
		case changed <- struct{}{}:
		default: // a reload is already pending
		}
	}
	// The subscription goroutine is tracked: Run returns only after its LISTEN connection is closed, so the
	// caller can safely close the pool afterwards.
	var listener sync.WaitGroup
	listener.Go(func() { k.listen(ctx, notify) })
	defer listener.Wait()

	ticker := time.NewTicker(k.refresh)
	defer ticker.Stop()
	for {
		if err := k.Reload(ctx); err != nil && ctx.Err() == nil {
			k.log.Error("keyring reload failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-changed:
		}
	}
}

// listen keeps a LISTEN subscription alive; the periodic refresh covers any gap.
func (k *Keyring) listen(ctx context.Context, notify func()) {
	for ctx.Err() == nil {
		if err := k.store.Subscribe(ctx, notify); err != nil && ctx.Err() == nil {
			k.log.Warn("signing_keys subscription lost", "error", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(subscribeRetryDelay):
		}
	}
}
