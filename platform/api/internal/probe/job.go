package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"time"
)

const (
	// DefaultInterval is how often every deployment host is probed (PORTAL_PROBE_INTERVAL).
	DefaultInterval = 5 * time.Minute
	// MinInterval and MaxInterval bound a configured interval; zero disables the probe.
	MinInterval = time.Minute
	MaxInterval = 24 * time.Hour
	// DefaultConcurrency is how many hosts are probed at the same time.
	DefaultConcurrency = 8
	// jitterFraction spreads the ticks of several replicas: each wait is the interval plus or minus this share.
	jitterFraction = 0.1
	// initialDelay is the base wait before the first round after start-up (jittered).
	initialDelay = time.Second
	// dueFraction is the share of the interval a probe result stays fresh. A replica that wakes up before
	// another one's results are this old finds nothing due, so one tick probes each host once even though every
	// replica runs the loop.
	dueFraction = 0.5
)

// Report counts what one round did.
type Report struct {
	// Skipped means another replica holds the lock of the round.
	Skipped bool
	// Inconclusive means the control sample failed (or the portal app has no deployment): nothing was stored.
	Inconclusive bool
	// Probed is how many hosts were probed.
	Probed int
	// Stored is how many results were written (a row locked by someone else or removed meanwhile is skipped).
	Stored int64
}

// Prober probes one target (Checker in production).
type Prober interface {
	Check(ctx context.Context, t Target) Result
}

// Store runs a round under a non-blocking cross-replica lock. It selects the targets whose last probe is older
// than dueAfter (or that were never probed) and, apart from them, the deployments of controlAppID (due or not).
// When something is due it passes both lists to run and stores the results it returns. With the lock held
// elsewhere it returns Skipped without calling run.
type Store interface {
	Round(ctx context.Context, dueAfter time.Duration, controlAppID string,
		run func(ctx context.Context, due, control []Target) []Result) (Report, error)
}

// Config wires a Job. Concurrency, Jitter and Logger are optional.
type Config struct {
	Store    Store
	Checker  Prober
	Interval time.Duration
	// ControlAppID is the app whose first deployment is probed before every round as the control sample: an app
	// known to sit behind ForwardAuth (the portal). If it does not answer "ok" the round proves nothing (an
	// outage of platform-api makes every host look unprotected) and nothing is stored.
	ControlAppID string
	Concurrency  int
	// Jitter returns the wait before the next round for the given interval; nil means interval plus or minus
	// 10 percent at random.
	Jitter func(interval time.Duration) time.Duration
	Logger *slog.Logger
}

// Job is the periodic probe.
type Job struct {
	store       Store
	checker     Prober
	interval    time.Duration
	controlApp  string
	concurrency int
	jitter      func(time.Duration) time.Duration
	log         *slog.Logger
}

// NewJob validates cfg and applies defaults. The interval must be positive: a disabled probe is simply not
// built.
func NewJob(cfg Config) (*Job, error) {
	if cfg.Store == nil || cfg.Checker == nil {
		return nil, errors.New("probe: store and checker are required")
	}
	if cfg.Interval <= 0 {
		return nil, errors.New("probe: the interval must be positive")
	}
	if cfg.ControlAppID == "" {
		return nil, errors.New("probe: the control app is required")
	}
	j := &Job{
		store: cfg.Store, checker: cfg.Checker, interval: cfg.Interval, concurrency: cfg.Concurrency,
		jitter: cfg.Jitter, log: cfg.Logger, controlApp: cfg.ControlAppID,
	}
	if j.concurrency <= 0 {
		j.concurrency = DefaultConcurrency
	}
	if j.jitter == nil {
		j.jitter = randomJitter
	}
	if j.log == nil {
		j.log = slog.Default()
	}
	return j, nil
}

// randomJitter returns interval plus or minus jitterFraction of it.
func randomJitter(interval time.Duration) time.Duration {
	spread := int64(float64(interval) * jitterFraction)
	if spread <= 0 {
		return interval
	}
	return interval + time.Duration(rand.Int64N(2*spread+1)-spread) //nolint:gosec // G404: scheduling jitter, not a secret
}

// RunOnce executes one round.
func (j *Job) RunOnce(ctx context.Context) (Report, error) {
	dueAfter := time.Duration(float64(j.interval) * dueFraction)
	var probed int
	var inconclusive bool
	rep, err := j.store.Round(ctx, dueAfter, j.controlApp, func(ctx context.Context, due, control []Target) []Result {
		if len(control) == 0 {
			j.log.Warn("probe round skipped: the control app has no deployment", "control_app_id", j.controlApp)
			inconclusive = true
			return nil
		}
		ctl := control[0]
		ctlRes := j.checker.Check(ctx, ctl) // if it is due as well, this is its probe: not repeated below
		if ctx.Err() != nil {
			return nil
		}
		if ctlRes.Status != StatusOK {
			j.log.Warn("probe round inconclusive: the control host is not behind ForwardAuth, no status stored",
				"control_status", string(ctlRes.Status), "control_host", ctl.Host, "http_status", ctlRes.Code)
			inconclusive = true
			return nil
		}
		rest := slices.DeleteFunc(slices.Clone(due), func(t Target) bool { return t.Host == ctl.Host })
		results := j.probeAll(ctx, rest)
		// The control again at the end: a platform outage that began during the round taints the results
		// already measured, so the whole round is discarded. Results are written once, after this check.
		if end := j.checker.Check(ctx, ctl); ctx.Err() != nil {
			return nil
		} else if end.Status != StatusOK {
			j.log.Warn("probe round discarded: the control host stopped answering ok during the round, no status stored",
				"control_status", string(end.Status), "control_host", ctl.Host, "http_status", end.Code)
			inconclusive = true
			return nil
		}
		for _, r := range results {
			j.report(r) // only now: results of a discarded round are not evidence of anything
		}
		if slices.ContainsFunc(due, func(t Target) bool { return t.Host == ctl.Host }) && ctx.Err() == nil {
			results = append(results, ctlRes) // ok, nothing to warn about
		}
		probed = len(results)
		return results
	})
	if err != nil {
		return rep, fmt.Errorf("probe: %w", err)
	}
	rep.Probed = probed
	rep.Inconclusive = inconclusive
	if !rep.Skipped && probed > 0 {
		j.log.Info("probe round done", "probed", probed, "stored", rep.Stored)
	}
	return rep, nil
}

// probeAll checks the targets with at most j.concurrency requests at a time. Results of probes cut short by the
// end of ctx are dropped: they say nothing about the host.
func (j *Job) probeAll(ctx context.Context, targets []Target) []Result {
	results := make([]Result, 0, len(targets))
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, j.concurrency)
	)
	for _, t := range targets {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			res := j.checker.Check(ctx, t)
			if ctx.Err() != nil {
				return
			}
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		})
	}
	wg.Wait()
	return results
}

// report logs the dangerous outcomes: host, app id and status code, never a response body.
func (j *Job) report(r Result) {
	switch r.Status {
	case StatusUnprotected, StatusUnexpected:
		j.log.Warn("probe: deployment is not protected by ForwardAuth",
			"status", string(r.Status), "host", r.Host, "app_id", r.AppID, "http_status", r.Code)
	case StatusOK, StatusUnreachable:
	}
}

// Run probes after a short random delay and then every interval (with jitter) until ctx ends. Errors are logged
// and retried on the next round.
func (j *Job) Run(ctx context.Context) {
	timer := time.NewTimer(j.jitter(initialDelay))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if _, err := j.RunOnce(ctx); err != nil && ctx.Err() == nil {
			j.log.Error("probe round failed", "error", err.Error())
		}
		timer.Reset(j.jitter(j.interval))
	}
}
