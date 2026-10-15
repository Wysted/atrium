package keys

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
)

// DefaultRotationInterval is how often Run ticks.
const DefaultRotationInterval = time.Minute

// Event records one change made by a tick.
type Event struct {
	Action ActionKind
	KID    string
}

// TickResult reports what a tick did.
type TickResult struct {
	Skipped bool // another replica holds the rotation lock
	Events  []Event
}

// RotatorConfig wires a Rotator. Logger is optional. There is no clock: time comes from the database (Tx.Now).
type RotatorConfig struct {
	Store  Store
	KEK    *KEK
	Logger *slog.Logger
}

// Rotator advances the key lifecycle. Every mutation happens under a PostgreSQL advisory lock, so any
// number of replicas can run it: ticks are idempotent and only one replica acts at a time.
type Rotator struct {
	store Store
	kek   *KEK
	log   *slog.Logger
}

// NewRotator validates cfg and applies defaults.
func NewRotator(cfg RotatorConfig) (*Rotator, error) {
	if cfg.Store == nil || cfg.KEK == nil {
		return nil, errors.New("rotator: store and KEK are required")
	}
	r := &Rotator{store: cfg.Store, kek: cfg.KEK, log: cfg.Logger}
	if r.log == nil {
		r.log = slog.Default()
	}
	return r, nil
}

// Tick runs one rotation step. If another replica holds the lock it returns Skipped without error.
func (r *Rotator) Tick(ctx context.Context) (TickResult, error) {
	var events []Event
	acquired, err := r.store.Locked(ctx, false, func(tx Tx) error {
		recs, err := tx.List(ctx)
		if err != nil {
			return err
		}
		// A wrong KEK looks exactly like corrupt keys; acting on it would destroy healthy keys.
		if err := checkKEK(r.kek, recs); err != nil {
			return err
		}
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		healthy, removals := r.dropCorruptNexts(recs)
		events, err = r.apply(ctx, tx, now, recs, append(removals, Plan(now, healthy)...))
		return err
	})
	if err != nil {
		return TickResult{}, fmt.Errorf("rotation tick: %w", err)
	}
	for _, e := range events {
		r.log.Info("signing key rotation", "action", e.Action, "kid", e.KID)
	}
	return TickResult{Skipped: !acquired, Events: events}, nil
}

// Run ticks immediately and then every interval until ctx ends. Errors are logged and retried next tick.
func (r *Rotator) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultRotationInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := r.Tick(ctx); err != nil && ctx.Err() == nil {
			r.log.Error("signing key rotation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Revoke is the emergency rotation: kid is removed (out of the JWKS, private material deleted) at once.
// If it was the active key, a published next key is promoted immediately, so apps already know the new
// signer: the oldest one, which is the one that has been published the longest (a next published for at
// least PrepublishDuration is therefore always preferred over a younger one). A next key that cannot be
// unsealed inside the transaction is skipped with an ERROR log and the following one is tried, because
// a promotion that no replica can load would leave the platform unable to sign. Only when no next is
// usable (the emergency window: bootstrap before the first plan, a next that was itself revoked, or
// every next unreadable) is a fresh active key created directly; apps then pay one rate-limited JWKS
// refetch. Revoking never leaves the compromised key active. A replacement next key is created whenever
// no usable one is left.
func (r *Rotator) Revoke(ctx context.Context, kid string) error {
	var events []Event
	_, err := r.store.Locked(ctx, true, func(tx Tx) error {
		recs, err := tx.List(ctx)
		if err != nil {
			return err
		}
		if err := checkKEK(r.kek, recs); err != nil {
			return err
		}
		idx := slices.IndexFunc(recs, func(rec Record) bool { return rec.KID == kid })
		if idx < 0 {
			return ErrKeyNotFound
		}
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		actions := []Action{{Kind: ActionRemove, KID: kid}}
		usableNexts := r.usableNexts(recs, kid)
		if recs[idx].State == StateActive {
			if len(usableNexts) > 0 {
				promote := usableNexts[0]
				if now.Sub(promote.CreatedAt) < PrepublishDuration {
					r.log.Warn("promoting a next key published for less than the prepublish window: no matured next exists",
						"kid", promote.KID)
				}
				actions = append(actions, Action{Kind: ActionPromote, KID: promote.KID})
				usableNexts = usableNexts[1:]
			} else {
				actions = append(actions, Action{Kind: ActionCreateActive})
			}
		}
		if len(usableNexts) == 0 {
			actions = append(actions, Action{Kind: ActionCreateNext})
		}
		events, err = r.apply(ctx, tx, now, recs, actions)
		return err
	})
	if err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}
	for _, e := range events {
		r.log.Warn("signing key emergency rotation", "action", e.Action, "kid", e.KID)
	}
	return nil
}

// dropCorruptNexts splits recs into the records the plan may consider and the removals for next keys that
// cannot be unsealed. It is only called after checkKEK passed, so the replica KEK is the one that sealed
// every active/next key and a failure to unseal means the key itself is corrupt: it is removed (ERROR
// log) and Plan, which no longer sees it, creates a replacement next key.
func (r *Rotator) dropCorruptNexts(recs []Record) (healthy []Record, removals []Action) {
	for _, rec := range recs {
		if rec.State == StateNext {
			if _, err := unseal(r.kek, rec); err != nil {
				r.log.Error("next signing key is corrupt (replica KEK matches the key's KEK): removing it", "kid", rec.KID, "error", err)
				removals = append(removals, Action{Kind: ActionRemove, KID: rec.KID})
				continue
			}
		}
		healthy = append(healthy, rec)
	}
	return healthy, removals
}

// usableNexts returns the next keys other than except that can be unsealed, oldest first (the oldest
// has been published the longest, so apps have had the most time to fetch it). Every next that cannot
// be unsealed is logged at ERROR: it is unusable and needs operator attention.
func (r *Rotator) usableNexts(recs []Record, except string) []Record {
	var nexts []Record
	for _, rec := range recs {
		if rec.State == StateNext && rec.KID != except {
			nexts = append(nexts, rec)
		}
	}
	slices.SortStableFunc(nexts, func(a, b Record) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return slices.DeleteFunc(nexts, func(rec Record) bool {
		if _, err := unseal(r.kek, rec); err != nil {
			r.log.Error("next signing key cannot be unsealed: skipped for promotion", "kid", rec.KID, "error", err)
			return true
		}
		return false
	})
}

// apply executes actions inside the open transaction; recs is the list read in that same transaction.
func (r *Rotator) apply(ctx context.Context, tx Tx, now time.Time, recs []Record, actions []Action) ([]Event, error) {
	events := make([]Event, 0, len(actions))
	for _, a := range actions {
		var err error
		switch a.Kind {
		case ActionCreateActive, ActionCreateNext:
			state := StateNext
			if a.Kind == ActionCreateActive {
				state = StateActive
			}
			var rec Record
			if rec, err = newRecord(r.kek, state, now); err == nil {
				err = tx.Insert(ctx, rec)
				a.KID = rec.KID
			}
		case ActionPromote:
			// Never promote a key this replica (and so any replica with the same KEK) cannot load.
			if err = r.checkUnseals(recs, a.KID); err == nil {
				err = tx.SetState(ctx, a.KID, StateActive, now)
			}
		case ActionRetire:
			err = tx.SetState(ctx, a.KID, StateRetired, now)
		case ActionRemove:
			err = tx.SetState(ctx, a.KID, StateRemoved, now)
		default:
			err = fmt.Errorf("unknown action %q", a.Kind)
		}
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", a.Kind, a.KID, err)
		}
		events = append(events, Event{Action: a.Kind, KID: a.KID})
	}
	return events, nil
}

// checkUnseals verifies that the private key of kid can be recovered with the KEK.
func (r *Rotator) checkUnseals(recs []Record, kid string) error {
	idx := slices.IndexFunc(recs, func(rec Record) bool { return rec.KID == kid })
	if idx < 0 {
		return ErrKeyNotFound
	}
	if _, err := unseal(r.kek, recs[idx]); err != nil {
		return fmt.Errorf("refusing to promote: %w", err)
	}
	return nil
}
