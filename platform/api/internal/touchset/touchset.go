// Package touchset is the core of lastseen and tokenuse: a bounded in-memory set of key -> latest time that
// request paths Touch without ever waiting for I/O, and one background writer (Run) flushes with a single write per
// tick and once more at shutdown. It keeps the database at one write per key per interval per replica.
package touchset

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultInterval is how often Run flushes.
	DefaultInterval = time.Minute
	// FlushTimeout bounds one flush.
	FlushTimeout = 10 * time.Second
	// FinalFlushTimeout bounds the best-effort flush at shutdown.
	FinalFlushTimeout = 5 * time.Second
	// MaxPending bounds the keys held between two flushes. Beyond it new keys are dropped (and counted) until the
	// next flush: best-effort telemetry never grows memory.
	MaxPending = 10_000
)

// Entry is the latest time observed for a key.
type Entry struct {
	Key string
	At  time.Time
}

// Config wires a Set. Interval, Now and Logger are optional.
type Config struct {
	// Name is the column written (e.g. "last_seen_at"); it labels errors and log lines.
	Name string
	// Write persists the batch in one statement. It returns the keys it could not write because their row was
	// locked by someone else (skipped, not failed): they stay in memory for the next tick.
	Write    func(ctx context.Context, batch []Entry) (skipped []string, err error)
	Interval time.Duration
	Now      func() time.Time
	Logger   *slog.Logger
}

// Set aggregates observations in memory. Touch is safe for concurrent use and never blocks on I/O.
type Set struct {
	cfg Config

	flushMu sync.Mutex // serializes flushes so a failed one restores its batch before the next starts

	mu      sync.Mutex
	pending map[string]time.Time

	dropped    atomic.Uint64
	flushError atomic.Uint64
}

// New validates cfg and applies defaults.
func New(cfg Config) (*Set, error) {
	if cfg.Write == nil {
		return nil, fmt.Errorf("touchset %s: write function is required", cfg.Name)
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Set{cfg: cfg, pending: map[string]time.Time{}}, nil
}

// Touch notes that key was just seen.
func (s *Set) Touch(key string) {
	at := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, known := s.pending[key]; !known && len(s.pending) >= MaxPending {
		s.dropped.Add(1)
		return
	}
	s.pending[key] = at
}

// Pending returns how many keys wait for the next flush.
func (s *Set) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// Dropped returns how many observations were discarded because MaxPending was reached, since start-up.
func (s *Set) Dropped() uint64 { return s.dropped.Load() }

// FlushErrors returns how many flushes failed, since start-up.
func (s *Set) FlushErrors() uint64 { return s.flushError.Load() }

// Flush writes the pending observations with one Write. On failure the batch goes back to memory (newer
// observations of the same key win) so the next tick retries it; so do the keys Write reports as skipped.
func (s *Set) Flush(ctx context.Context) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.mu.Lock()
	batch := s.pending
	s.pending = map[string]time.Time{}
	s.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}

	entries := make([]Entry, 0, len(batch))
	for key, at := range batch {
		entries = append(entries, Entry{Key: key, At: at})
	}
	skipped, err := s.cfg.Write(ctx, entries)
	if err != nil {
		s.restore(batch)
		s.flushError.Add(1)
		return fmt.Errorf("%s: flush %d entries: %w", s.cfg.Name, len(batch), err)
	}
	if len(skipped) > 0 {
		retry := make(map[string]time.Time, len(skipped))
		for _, key := range skipped {
			if at, ok := batch[key]; ok {
				retry[key] = at
			}
		}
		s.restore(retry)
	}
	return nil
}

// restore puts observations back without overwriting newer ones or exceeding MaxPending.
func (s *Set) restore(batch map[string]time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, at := range batch {
		cur, known := s.pending[key]
		switch {
		case known && !at.After(cur):
		case !known && len(s.pending) >= MaxPending:
			s.dropped.Add(1)
		default:
			s.pending[key] = at
		}
	}
}

// Run flushes every interval until ctx ends, then makes one last best-effort flush. A failed flush is logged and
// retried on the next tick; it never affects a decision.
func (s *Set) Run(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.finalFlush(ctx)
			return
		case <-ticker.C:
			fctx, cancel := context.WithTimeout(ctx, FlushTimeout)
			if err := s.Flush(fctx); err != nil && ctx.Err() == nil {
				s.cfg.Logger.Error("cannot flush "+s.cfg.Name, "error", err.Error(), "pending", s.Pending())
			}
			cancel()
		}
	}
}

func (s *Set) finalFlush(parent context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), FinalFlushTimeout)
	defer cancel()
	if err := s.Flush(ctx); err != nil {
		s.cfg.Logger.Warn("cannot flush "+s.cfg.Name+" at shutdown", "error", err.Error())
	}
}

// snapshot copies the pending set (tests).
func (s *Set) snapshot() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.pending)
}
