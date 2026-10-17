// Package tokenuse records when API tokens are used (api_tokens.last_used_at, contract block 6, "Non-browser
// clients"). ForwardAuth calls Touch on every authorized token request; the Recorder keeps the latest time per token
// in a bounded in-memory set and one background writer (Run) flushes it with one statement per tick, so the database
// sees at most one write per minute per token per replica and ForwardAuth never waits for it. It mirrors lastseen,
// which does the same for deployments.
package tokenuse

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultInterval is how often Run flushes: at most one write per minute per token.
	DefaultInterval = time.Minute
	// FlushTimeout bounds one flush statement.
	FlushTimeout = 10 * time.Second
	// FinalFlushTimeout bounds the best-effort flush at shutdown.
	FinalFlushTimeout = 5 * time.Second
	// MaxPending bounds the tokens held between two flushes. Only tokens that passed every check are recorded, so
	// reaching it takes that many distinct live tokens used within a minute; beyond it new tokens are dropped
	// (and counted) until the next flush: best-effort telemetry never grows memory.
	MaxPending = 10_000
)

// Used is the latest authorized request observed with a token.
type Used struct {
	ID string
	At time.Time
}

// Store writes the observations in one statement. It must never move last_used_at backwards, and a token that is
// gone or locked is not an error: the write is best effort.
type Store interface {
	MarkUsed(ctx context.Context, used []Used) error
}

// Config wires a Recorder. Interval, Now and Logger are optional.
type Config struct {
	Store    Store
	Interval time.Duration
	Now      func() time.Time
	Logger   *slog.Logger
}

// Recorder aggregates observations in memory. Touch is safe for concurrent use and never blocks on I/O.
type Recorder struct {
	store    Store
	interval time.Duration
	now      func() time.Time
	log      *slog.Logger

	flushMu sync.Mutex // serializes flushes so a failed one restores its batch before the next starts

	mu      sync.Mutex
	pending map[string]time.Time
	dropped atomic.Uint64
}

// New validates cfg and applies defaults.
func New(cfg Config) (*Recorder, error) {
	if cfg.Store == nil {
		return nil, errors.New("tokenuse: store is required")
	}
	r := &Recorder{store: cfg.Store, interval: cfg.Interval, now: cfg.Now, log: cfg.Logger, pending: map[string]time.Time{}}
	if r.interval <= 0 {
		r.interval = DefaultInterval
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	return r, nil
}

// Touch notes that the token id just authorized a request.
func (r *Recorder) Touch(id string) {
	at := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, known := r.pending[id]; !known && len(r.pending) >= MaxPending {
		r.dropped.Add(1)
		return
	}
	r.pending[id] = at
}

// Pending returns how many tokens wait for the next flush.
func (r *Recorder) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

// Dropped returns how many observations were discarded because MaxPending was reached, since start-up.
func (r *Recorder) Dropped() uint64 { return r.dropped.Load() }

// Flush writes the pending observations with one statement. On failure the batch goes back to memory (newer
// observations of the same token win) so the next tick retries it.
func (r *Recorder) Flush(ctx context.Context) error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()

	r.mu.Lock()
	batch := r.pending
	r.pending = map[string]time.Time{}
	r.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	used := make([]Used, 0, len(batch))
	for id, at := range batch {
		used = append(used, Used{ID: id, At: at})
	}
	if err := r.store.MarkUsed(ctx, used); err != nil {
		r.restore(batch)
		return fmt.Errorf("tokenuse: flush %d tokens: %w", len(batch), err)
	}
	return nil
}

// restore puts a failed batch back without overwriting newer observations or exceeding MaxPending.
func (r *Recorder) restore(batch map[string]time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, at := range batch {
		cur, known := r.pending[id]
		switch {
		case known && !at.After(cur):
		case !known && len(r.pending) >= MaxPending:
			r.dropped.Add(1)
		default:
			r.pending[id] = at
		}
	}
}

// Run flushes every interval until ctx ends, then makes one last best-effort flush. A failed flush is logged and
// retried on the next tick; it never affects a decision.
func (r *Recorder) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.finalFlush(ctx)
			return
		case <-ticker.C:
			fctx, cancel := context.WithTimeout(ctx, FlushTimeout)
			if err := r.Flush(fctx); err != nil && ctx.Err() == nil {
				r.log.Error("cannot flush last_used_at", "error", err.Error(), "pending", r.Pending())
			}
			cancel()
		}
	}
}

func (r *Recorder) finalFlush(parent context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), FinalFlushTimeout)
	defer cancel()
	if err := r.Flush(ctx); err != nil {
		r.log.Warn("cannot flush last_used_at at shutdown", "error", err.Error())
	}
}

// snapshot copies the pending set (tests).
func (r *Recorder) snapshot() map[string]time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.pending)
}
