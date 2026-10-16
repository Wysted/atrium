// Package lastseen records which deployments are receiving authorized traffic (contract block 6,
// "Observability"). ForwardAuth calls Touch on every authorized decision; the Recorder keeps the latest time per
// host in memory and a background flusher writes them with one statement per tick, so the database sees at most
// one write per minute per deployment and never one per request.
package lastseen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"
)

const (
	// DefaultInterval is how often Run flushes (contract block 6: at most once per minute per deployment).
	DefaultInterval = time.Minute
	// FlushTimeout bounds one flush statement.
	FlushTimeout = 10 * time.Second
	// FinalFlushTimeout bounds the best-effort flush at shutdown.
	FinalFlushTimeout = 5 * time.Second
	// MaxPending bounds the hosts held between two flushes. Only hosts that resolved to a deployment are
	// recorded, so the bound is never reached unless thousands of deployments exist; beyond it new hosts are
	// dropped until the next flush (best-effort telemetry, never a reason to grow memory).
	MaxPending = 10_000
)

// Seen is the latest authorized request observed on a host.
type Seen struct {
	Host string
	At   time.Time
}

// Store writes the observations in one statement. It must never move last_seen_at backwards. It returns the hosts
// it could not write because their row was locked by someone else (skipped, not failed): the Recorder keeps
// them for the next tick. Hosts that have no deployment, or whose row already holds a newer time, are done.
type Store interface {
	MarkSeen(ctx context.Context, seen []Seen) (skipped []string, err error)
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
}

// New validates cfg and applies defaults.
func New(cfg Config) (*Recorder, error) {
	if cfg.Store == nil {
		return nil, errors.New("lastseen: store is required")
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

// Touch notes that host just received an authorized request. host must be a known deployment host.
func (r *Recorder) Touch(host string) {
	at := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, known := r.pending[host]; !known && len(r.pending) >= MaxPending {
		return
	}
	r.pending[host] = at
}

// Pending returns how many hosts wait for the next flush.
func (r *Recorder) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

// Flush writes the pending observations with one statement. On failure the batch goes back to memory (newer
// observations of the same host win) so the next tick retries it.
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

	seen := make([]Seen, 0, len(batch))
	for host, at := range batch {
		seen = append(seen, Seen{Host: host, At: at})
	}
	skipped, err := r.store.MarkSeen(ctx, seen)
	if err != nil {
		r.restore(batch)
		return fmt.Errorf("lastseen: flush %d hosts: %w", len(batch), err)
	}
	if len(skipped) > 0 {
		retry := make(map[string]time.Time, len(skipped))
		for _, host := range skipped {
			if at, ok := batch[host]; ok {
				retry[host] = at
			}
		}
		r.restore(retry)
	}
	return nil
}

// restore puts observations back (a failed batch, or the rows that were locked) without overwriting newer observations or exceeding MaxPending.
func (r *Recorder) restore(batch map[string]time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for host, at := range batch {
		cur, known := r.pending[host]
		switch {
		case known && !at.After(cur):
		case !known && len(r.pending) >= MaxPending:
		default:
			r.pending[host] = at
		}
	}
}

// Run flushes every interval until ctx ends, then makes one last best-effort flush. A failed flush is logged
// and retried on the next tick; it never affects a decision.
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
				r.log.Error("cannot flush last_seen_at", "error", err.Error(), "pending", r.Pending())
			}
			cancel()
		}
	}
}

func (r *Recorder) finalFlush(parent context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), FinalFlushTimeout)
	defer cancel()
	if err := r.Flush(ctx); err != nil {
		r.log.Warn("cannot flush last_seen_at at shutdown", "error", err.Error())
	}
}

// snapshot copies the pending set (tests).
func (r *Recorder) snapshot() map[string]time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.pending)
}
