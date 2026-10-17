// Package lastseen records which deployments are receiving authorized traffic (contract block 6,
// "Observability"). ForwardAuth calls Touch on every authorized decision; the Recorder keeps the latest time per
// host in memory and a background flusher writes them with one statement per tick, so the database sees at most
// one write per minute per deployment and never one per request. The aggregation and flushing live in touchset.
package lastseen

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"platform/api/internal/touchset"
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

// Config wires a Recorder. Now and Logger are optional.
type Config struct {
	Store  Store
	Now    func() time.Time
	Logger *slog.Logger
}

// Recorder aggregates observations per deployment host. Touch(host) is safe for concurrent use and never blocks
// on I/O; host must be a known deployment host. Pending, Dropped, FlushErrors, Flush and Run come from touchset.
type Recorder struct{ *touchset.Set }

// New validates cfg and applies defaults.
func New(cfg Config) (*Recorder, error) {
	if cfg.Store == nil {
		return nil, errors.New("lastseen: store is required")
	}
	set, err := touchset.New(touchset.Config{
		Name: "last_seen_at", Now: cfg.Now, Logger: cfg.Logger,
		Write: func(ctx context.Context, batch []touchset.Entry) ([]string, error) {
			seen := make([]Seen, len(batch))
			for i, e := range batch {
				seen[i] = Seen{Host: e.Key, At: e.At}
			}
			return cfg.Store.MarkSeen(ctx, seen)
		},
	})
	if err != nil {
		return nil, err
	}
	return &Recorder{set}, nil
}
