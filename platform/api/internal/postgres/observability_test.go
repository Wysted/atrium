package postgres_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/lastseen"
	"platform/api/internal/postgres"
	"platform/api/internal/probe"
	"platform/api/internal/testdb"
)

// listenAccessChanged returns a function that reports the payload of the next access_changed notification.
func listenAccessChanged(t *testing.T, owner *pgxpool.Pool) func() string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	conn, err := owner.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Release)
	if _, err := conn.Exec(ctx, "LISTEN access_changed"); err != nil {
		t.Fatal(err)
	}
	return func() string {
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		defer wcancel()
		n, err := conn.Conn().WaitForNotification(wctx)
		if err != nil {
			t.Fatalf("no notification: %v", err)
		}
		return n.Payload
	}
}

func seenAt(t *testing.T, pool *pgxpool.Pool, host string) time.Time {
	t.Helper()
	return scalar[*time.Time](t, pool, `SELECT last_seen_at FROM deployments WHERE host = $1`, host).UTC()
}

func TestMarkSeen(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	rt := runtimePool(t, owner)
	w := world{pool: owner}
	in := w.initiative(t, "alpha", "active")
	w.app(t, in, "a.example.test")
	w.app(t, in, "b.example.test")
	repo := postgres.NewDeploymentRepo(rt)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	next := listenAccessChanged(t, owner)

	t.Run("sets the time of the hosts named and ignores unknown ones", func(t *testing.T) {
		at := time.Now().Add(-time.Minute).Truncate(time.Microsecond)
		if _, err := repo.MarkSeen(ctx, []lastseen.Seen{{Host: "a.example.test", At: at}, {Host: "gone.example.test", At: at}}); err != nil {
			t.Fatal(err)
		}
		if got := seenAt(t, owner, "a.example.test"); !got.Equal(at) {
			t.Fatalf("a seen at %s, want %s", got, at)
		}
		if n := scalar[int](t, owner, `SELECT count(*) FROM deployments WHERE host = 'b.example.test' AND last_seen_at IS NULL`); n != 1 {
			t.Fatal("a host that was not named got a time")
		}
	})

	t.Run("never moves backwards", func(t *testing.T) {
		before := seenAt(t, owner, "a.example.test")
		if _, err := repo.MarkSeen(ctx, []lastseen.Seen{{Host: "a.example.test", At: before.Add(-time.Hour)}}); err != nil {
			t.Fatal(err)
		}
		if got := seenAt(t, owner, "a.example.test"); !got.Equal(before) {
			t.Fatalf("last_seen_at moved from %s to %s", before, got)
		}
		later := before.Add(10 * time.Second)
		if _, err := repo.MarkSeen(ctx, []lastseen.Seen{{Host: "a.example.test", At: later}}); err != nil {
			t.Fatal(err)
		}
		if got := seenAt(t, owner, "a.example.test"); !got.Equal(later) {
			t.Fatalf("last_seen_at = %s, want %s", got, later)
		}
	})

	t.Run("a skewed clock cannot write the future", func(t *testing.T) {
		if _, err := repo.MarkSeen(ctx, []lastseen.Seen{{Host: "b.example.test", At: time.Now().Add(24 * time.Hour)}}); err != nil {
			t.Fatal(err)
		}
		if got := seenAt(t, owner, "b.example.test"); got.After(time.Now().Add(time.Minute)) {
			t.Fatalf("last_seen_at = %s is in the future", got)
		}
	})

	t.Run("an empty batch is a no-op", func(t *testing.T) {
		if _, err := repo.MarkSeen(ctx, nil); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a row locked by someone else is skipped, not waited for", func(t *testing.T) {
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		if _, err := tx.Exec(ctx, `SELECT 1 FROM deployments WHERE host = 'a.example.test' FOR UPDATE`); err != nil {
			t.Fatal(err)
		}
		before := seenAt(t, owner, "a.example.test")
		start := time.Now()
		skipped, err := repo.MarkSeen(ctx, []lastseen.Seen{{Host: "a.example.test", At: time.Now()}, {Host: "gone.example.test", At: time.Now()}})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(skipped, []string{"a.example.test"}) {
			t.Fatalf("skipped = %v, want only the locked host (not the one without deployment)", skipped)
		}
		if time.Since(start) > 3*time.Second {
			t.Fatalf("MarkSeen waited %s for a locked row", time.Since(start))
		}
		if got := seenAt(t, owner, "a.example.test"); !got.Equal(before) {
			t.Fatal("a locked row was written")
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.MarkSeen(ctx, []lastseen.Seen{{Host: "a.example.test", At: time.Now()}}); err != nil {
			t.Fatal(err)
		}
		if got := seenAt(t, owner, "a.example.test"); !got.After(before) {
			t.Fatal("the next flush did not write the row")
		}
		// A row that already holds a newer time is done, not skipped.
		skipped, err = repo.MarkSeen(ctx, []lastseen.Seen{{Host: "a.example.test", At: before.Add(-time.Hour)}})
		if err != nil || len(skipped) != 0 {
			t.Fatalf("an older observation was skipped: %v, %v", skipped, err)
		}
	})

	// Every statement above ran as platform_runtime. A sentinel on apps after them: the first notification
	// must be the sentinel's, so no flush emitted access_changed.
	mustExec(t, owner, `UPDATE apps SET name = name`)
	if got := next(); got != "apps" {
		t.Fatalf("first notification payload = %q, want apps: a last_seen_at write notified", got)
	}
}

func TestProbeRound(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	portalPool := testdb.Portal(t, owner)
	w := world{pool: owner}
	in := w.initiative(t, "alpha", "active")
	appA := w.app(t, in, "a.example.test")
	w.app(t, in, "b.example.test")
	w.app(t, in, "c.example.test")
	repo := postgres.NewProbeRepo(portalPool)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	next := listenAccessChanged(t, owner)

	var gotTargets, gotControl []probe.Target
	statuses := map[string]probe.Status{
		"a.example.test": probe.StatusOK, "b.example.test": probe.StatusUnprotected, "c.example.test": probe.StatusUnreachable,
	}
	run := func(_ context.Context, targets, control []probe.Target) []probe.Result {
		gotTargets, gotControl = targets, control
		var out []probe.Result
		for _, tg := range targets {
			out = append(out, probe.Result{Target: tg, Status: statuses[tg.Host]})
		}
		return out
	}

	rep, err := repo.Round(ctx, time.Minute, appA, run)
	if err != nil || rep.Skipped || rep.Stored != 3 {
		t.Fatalf("round = %+v, %v", rep, err)
	}
	if len(gotTargets) != 3 || gotTargets[0].Host != "a.example.test" || gotTargets[0].AppID != appA {
		t.Fatalf("targets = %+v, want the three hosts with their app ids", gotTargets)
	}
	for host, want := range statuses {
		if got := scalar[string](t, owner, `SELECT last_probe_status FROM deployments WHERE host = $1`, host); got != string(want) {
			t.Fatalf("%s status = %q, want %q", host, got, want)
		}
		if scalar[*time.Time](t, owner, `SELECT last_probe_at FROM deployments WHERE host = $1`, host) == nil {
			t.Fatalf("%s has no last_probe_at", host)
		}
	}

	if len(gotControl) != 1 || gotControl[0].Host != "a.example.test" {
		t.Fatalf("control = %+v, want the deployments of the control app", gotControl)
	}

	t.Run("fresh results are not probed again", func(t *testing.T) {
		gotTargets = nil
		rep, err := repo.Round(ctx, time.Minute, appA, run)
		if err != nil || rep.Skipped || rep.Stored != 0 || gotTargets != nil {
			t.Fatalf("round = %+v, %v, targets %v", rep, err, gotTargets)
		}
	})

	t.Run("only the stale host is probed", func(t *testing.T) {
		mustExec(t, owner, `UPDATE deployments SET last_probe_at = now() - interval '10 minutes' WHERE host = 'b.example.test'`)
		statuses["b.example.test"] = probe.StatusOK
		rep, err := repo.Round(ctx, time.Minute, appA, run)
		if err != nil || rep.Stored != 1 || len(gotTargets) != 1 || gotTargets[0].Host != "b.example.test" {
			t.Fatalf("round = %+v, %v, targets %v", rep, err, gotTargets)
		}
		if len(gotControl) != 1 || gotControl[0].Host != "a.example.test" {
			t.Fatalf("control = %+v: it is listed even when it is not due", gotControl)
		}
		if got := scalar[string](t, owner, `SELECT last_probe_status FROM deployments WHERE host = 'b.example.test'`); got != "ok" {
			t.Fatalf("b status = %q", got)
		}
	})

	t.Run("a lock held by another replica skips the round", func(t *testing.T) {
		mustExec(t, owner, `UPDATE deployments SET last_probe_at = NULL`)
		holder, err := owner.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer holder.Release()
		if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, postgres.ProbeLockKey); err != nil {
			t.Fatal(err)
		}
		gotTargets = nil
		rep, err := repo.Round(ctx, time.Minute, appA, run)
		if err != nil || !rep.Skipped || gotTargets != nil {
			t.Fatalf("round = %+v, %v, targets %v", rep, err, gotTargets)
		}
		if n := scalar[int](t, owner, `SELECT count(*) FROM deployments WHERE last_probe_at IS NOT NULL`); n != 0 {
			t.Fatal("a skipped round wrote results")
		}
		if _, err := holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, postgres.ProbeLockKey); err != nil {
			t.Fatal(err)
		}
		rep, err = repo.Round(ctx, time.Minute, appA, run)
		if err != nil || rep.Skipped || rep.Stored != 3 {
			t.Fatalf("round after the lock was released = %+v, %v", rep, err)
		}
	})

	t.Run("the lock is released when the round ends", func(t *testing.T) {
		if !scalar[bool](t, owner, `SELECT pg_try_advisory_lock($1)`, postgres.ProbeLockKey) {
			t.Fatal("the lock is still held after the round")
		}
		mustExec(t, owner, `SELECT pg_advisory_unlock($1)`, postgres.ProbeLockKey)
	})

	t.Run("two replicas never run the same round", func(t *testing.T) {
		mustExec(t, owner, `UPDATE deployments SET last_probe_at = NULL`)
		entered, release := make(chan struct{}), make(chan struct{})
		var (
			mu    sync.Mutex
			calls int
		)
		slow := func(ctx context.Context, targets, control []probe.Target) []probe.Result {
			mu.Lock()
			calls++
			mu.Unlock()
			close(entered)
			<-release
			return run(ctx, targets, control)
		}
		var wg sync.WaitGroup
		var first probe.Report
		var firstErr error
		wg.Go(func() { first, firstErr = repo.Round(ctx, time.Minute, appA, slow) })
		<-entered
		second, err := repo.Round(ctx, time.Minute, appA, slow)
		close(release)
		wg.Wait()
		if err != nil || !second.Skipped || firstErr != nil || first.Stored != 3 {
			t.Fatalf("second %+v %v, first %+v %v", second, err, first, firstErr)
		}
		if calls != 1 {
			t.Fatalf("the round ran %d times", calls)
		}
	})

	// Probe results are telemetry: they must not flush the ForwardAuth caches.
	mustExec(t, owner, `UPDATE apps SET name = name`)
	if got := next(); got != "apps" {
		t.Fatalf("first notification payload = %q, want apps: a probe write notified", got)
	}
}

func TestProbeStatusCounts(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	portalPool := testdb.Portal(t, owner)
	w := world{pool: owner}
	in := w.initiative(t, "alpha", "active")
	w.app(t, in, "a.example.test")
	w.app(t, in, "b.example.test")
	w.app(t, in, "c.example.test")
	w.app(t, in, "d.example.test")
	repo := postgres.NewProbeRepo(portalPool)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	counts, err := repo.StatusCounts(ctx)
	if err != nil || len(counts) != 1 || counts[probe.StatusNever] != 4 {
		t.Fatalf("counts = %v, %v, want the four deployments as never probed", counts, err)
	}

	mustExec(t, owner, `UPDATE deployments SET last_probe_status = 'ok', last_probe_at = now() WHERE host IN ('a.example.test', 'b.example.test')`)
	mustExec(t, owner, `UPDATE deployments SET last_probe_status = 'unprotected', last_probe_at = now() WHERE host = 'c.example.test'`)
	counts, err = repo.StatusCounts(ctx)
	want := map[probe.Status]int{probe.StatusOK: 2, probe.StatusUnprotected: 1, probe.StatusNever: 1}
	if err != nil || len(counts) != len(want) {
		t.Fatalf("counts = %v, %v, want %v", counts, err, want)
	}
	for status, n := range want {
		if counts[status] != n {
			t.Fatalf("counts = %v, want %v", counts, want)
		}
	}
}

func TestPortalPoolIsLargeEnoughForTheProbe(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ configured, want int32 }{{4, postgres.MinPortalPoolConns}, {1, postgres.MinPortalPoolConns}, {32, 32}} {
		cfg, err := pgxpool.ParseConfig("")
		if err != nil {
			t.Fatal(err)
		}
		cfg.MaxConns = tt.configured
		postgres.WithMinConns(cfg)
		if cfg.MaxConns != tt.want {
			t.Errorf("configured %d -> %d, want %d", tt.configured, cfg.MaxConns, tt.want)
		}
	}
}
