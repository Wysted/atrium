// Package tokenuse records when API tokens are used (api_tokens.last_used_at, contract block 6, "Non-browser
// clients"). ForwardAuth calls Touch on every authorized token request; the Recorder keeps the latest time per token
// in a bounded in-memory set and one background writer (Run) flushes it with one statement per tick, so the database
// sees at most one write per minute per token per replica and ForwardAuth never waits for it. It mirrors lastseen,
// which does the same for deployments; the aggregation and flushing live in touchset.
package tokenuse

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/touchset"
)

// Used is the latest authorized request observed with a token.
type Used struct {
	ID string
	At time.Time
}

// Store writes the observations in one statement. It must never move last_used_at backwards. It returns the ids it
// could not write because their row was locked by someone else (skipped, not failed): the Recorder keeps them for
// the next tick. A token that is gone, or whose row already holds a newer time, is done.
type Store interface {
	MarkUsed(ctx context.Context, used []Used) (skipped []string, err error)
}

// Config wires a Recorder. Now and Logger are optional.
type Config struct {
	Store  Store
	Now    func() time.Time
	Logger *slog.Logger
}

// Recorder aggregates observations per token. Touch(id) is safe for concurrent use and never blocks on I/O.
// Pending, Dropped, FlushErrors, Flush and Run come from touchset.
type Recorder struct{ *touchset.Set }

// New validates cfg and applies defaults.
func New(cfg Config) (*Recorder, error) {
	if cfg.Store == nil {
		return nil, errors.New("tokenuse: store is required")
	}
	set, err := touchset.New(touchset.Config{
		Name: "last_used_at", Now: cfg.Now, Logger: cfg.Logger,
		Write: func(ctx context.Context, batch []touchset.Entry) ([]string, error) {
			used := make([]Used, len(batch))
			for i, e := range batch {
				used[i] = Used{ID: e.Key, At: e.At}
			}
			return cfg.Store.MarkUsed(ctx, used)
		},
	})
	if err != nil {
		return nil, err
	}
	return &Recorder{set}, nil
}
