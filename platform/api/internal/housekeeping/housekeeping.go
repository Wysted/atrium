// Package housekeeping runs the periodic maintenance of block 5: purging expired login state and creating
// access_denials partitions ahead of time. The job is idempotent and safe on any number of replicas.
package housekeeping

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/session"
)

const (
	// DefaultInterval is how often Run executes the job.
	DefaultInterval = 10 * time.Minute
	// PartitionMonthsAhead is how many months past the current one get an access_denials partition.
	PartitionMonthsAhead = 2
	// AccessDenialsRetentionDays is how long access_denials history is kept (contract block 4): monthly
	// partitions whose whole month is older than this are dropped.
	AccessDenialsRetentionDays = 365
	// APITokenGrace is how long a token is kept after it expired or was revoked (contract block 6), so owners
	// still see recently dead tokens. The database function refuses anything under 7 days.
	APITokenGrace = 30 * 24 * time.Hour
)

// Params are the rules a store applies with the database clock.
type Params struct {
	IdleTimeout          time.Duration // sessions idle for longer are purged
	PartitionMonthsAhead int
	DenialsRetentionDays int           // partitions entirely older than this are dropped
	APITokenGrace        time.Duration // API tokens dead for longer than this are deleted
}

// Report counts what one run did.
type Report struct {
	Skipped           bool // another replica holds the lock
	OAuthFlows        int64
	LoginCodes        int64
	Sessions          int64
	AppSessions       int64
	APITokens         int64 // dead API tokens deleted
	PartitionsReady   []string
	PartitionsDropped []string
}

// Store performs one run under a non-blocking cross-replica lock; with the lock taken elsewhere it returns Skipped.
type Store interface {
	Housekeep(ctx context.Context, p Params) (Report, error)
}

// Config wires a Job. Interval and Logger are optional.
type Config struct {
	Store       Store
	IdleTimeout time.Duration
	Interval    time.Duration
	Logger      *slog.Logger
}

// Job is the housekeeping job.
type Job struct {
	store    Store
	params   Params
	interval time.Duration
	log      *slog.Logger
}

// NewJob validates cfg and applies defaults.
func NewJob(cfg Config) (*Job, error) {
	if cfg.Store == nil {
		return nil, errors.New("housekeeping: store is required")
	}
	j := &Job{store: cfg.Store, interval: cfg.Interval, log: cfg.Logger}
	j.params = Params{IdleTimeout: cfg.IdleTimeout, PartitionMonthsAhead: PartitionMonthsAhead,
		DenialsRetentionDays: AccessDenialsRetentionDays, APITokenGrace: APITokenGrace}
	if j.params.IdleTimeout <= 0 {
		j.params.IdleTimeout = session.DefaultIdleTimeout
	}
	if j.interval <= 0 {
		j.interval = DefaultInterval
	}
	if j.log == nil {
		j.log = slog.Default()
	}
	return j, nil
}

// RunOnce executes one pass.
func (j *Job) RunOnce(ctx context.Context) (Report, error) {
	rep, err := j.store.Housekeep(ctx, j.params)
	if err != nil {
		return rep, fmt.Errorf("housekeeping: %w", err)
	}
	if !rep.Skipped {
		j.log.Info("housekeeping done", "oauth_flows", rep.OAuthFlows, "login_codes", rep.LoginCodes,
			"sessions", rep.Sessions, "app_sessions", rep.AppSessions, "api_tokens", rep.APITokens, "partitions", rep.PartitionsReady,
			"partitions_dropped", rep.PartitionsDropped)
	}
	return rep, nil
}

// Run executes the job immediately and then every interval until ctx ends. Errors are logged and retried.
func (j *Job) Run(ctx context.Context) {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		if _, err := j.RunOnce(ctx); err != nil && ctx.Err() == nil {
			j.log.Error("housekeeping failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
