package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Wysted/atrium/platform/api/internal/housekeeping"
	"github.com/Wysted/atrium/platform/api/internal/postgres"
)

const hkHost = "hk.example.com"

type hkFixture struct {
	pool                                *pgxpool.Pool
	userID, liveSession, expiredSession string
	idleSession, appID                  string
}

// seedHousekeeping inserts live and dead rows of every purged table.
func seedHousekeeping(t *testing.T, pool *pgxpool.Pool) hkFixture {
	t.Helper()
	w := world{pool: pool}
	f := hkFixture{pool: pool}
	f.userID = w.user(t, "active")
	ini := w.initiative(t, "hk", "active")
	f.appID = w.app(t, ini, hkHost)

	session := func(expires, lastSeen string) string {
		return scalar[string](t, pool, `INSERT INTO sessions (token_hash, user_id, expires_at, last_seen_at)
			VALUES (sha256(gen_random_uuid()::text::bytea), $1, now() + $2::interval, now() + $3::interval) RETURNING id::text`,
			f.userID, expires, lastSeen)
	}
	f.liveSession = session("1 hour", "-1 minute")
	f.expiredSession = session("-1 second", "0")
	f.idleSession = session("1 hour", "-2 hours")

	appSession := func(sid, expires string) {
		mustExec(t, pool, `INSERT INTO app_sessions (token_hash, session_id, app_id, host, expires_at)
			VALUES (sha256(gen_random_uuid()::text::bytea), $1, $2, $3, now() + $4::interval)`, sid, f.appID, hkHost, expires)
	}
	appSession(f.liveSession, "1 hour")
	appSession(f.liveSession, "-1 second") // expired app session of a live session: purged on its own
	appSession(f.expiredSession, "1 hour") // goes with its session by cascade

	code := func(sid, expires string) {
		mustExec(t, pool, `INSERT INTO login_codes (code_hash, session_id, app_id, host, bind_hash, rd, expires_at)
			VALUES (sha256(gen_random_uuid()::text::bytea), $1, $2, $3, sha256('b'), 'https://hk.example.com/', now() + $4::interval)`,
			sid, f.appID, hkHost, expires)
	}
	code(f.liveSession, "30 seconds")
	code(f.liveSession, "-1 second")

	flow := func(expires string) {
		mustExec(t, pool, `INSERT INTO oauth_flows (token_hash, state, nonce, code_verifier, rd, bind_hash, expires_at)
			VALUES (sha256(gen_random_uuid()::text::bytea), 's', 'n', 'v', 'https://hk.example.com/', sha256('b'), now() + $1::interval)`, expires)
	}
	flow("10 minutes")
	flow("-1 second")
	return f
}

func newJob(t *testing.T, pool *pgxpool.Pool, interval time.Duration) *housekeeping.Job {
	t.Helper()
	job, err := housekeeping.NewJob(housekeeping.Config{Store: postgres.NewHousekeepingRepo(pool), Interval: interval})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func rowCounts(t *testing.T, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, tbl := range []string{"sessions", "app_sessions", "login_codes", "oauth_flows"} {
		out[tbl] = scalar[int](t, pool, "SELECT count(*) FROM "+tbl)
	}
	return out
}

func TestHousekeepingPurgesExpiredRowsAndIsIdempotent(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	f := seedHousekeeping(t, pool)
	job := newJob(t, pool, 0)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	rep, err := job.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped {
		t.Fatal("skipped without contention")
	}
	// Sessions: expired + idle. App sessions: the expired one of the live session (explicit) and the one cascaded
	// from the expired session (counted by the cascade, not by the explicit DELETE). Codes/flows: one expired each.
	if rep.Sessions != 2 || rep.AppSessions != 1 || rep.LoginCodes != 1 || rep.OAuthFlows != 1 {
		t.Fatalf("report = %+v", rep)
	}
	want := map[string]int{"sessions": 1, "app_sessions": 1, "login_codes": 1, "oauth_flows": 1}
	for tbl, n := range rowCounts(t, pool) {
		if n != want[tbl] {
			t.Fatalf("%s rows = %d, want %d", tbl, n, want[tbl])
		}
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM sessions WHERE id = $1`, f.liveSession); n != 1 {
		t.Fatal("the live session was purged")
	}

	again, err := job.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Sessions+again.AppSessions+again.LoginCodes+again.OAuthFlows != 0 {
		t.Fatalf("second run purged more: %+v", again)
	}
	for tbl, n := range rowCounts(t, pool) {
		if n != want[tbl] {
			t.Fatalf("after second run %s rows = %d, want %d", tbl, n, want[tbl])
		}
	}
}

func TestHousekeepingCreatesPartitionsTwoMonthsAhead(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// Two months ahead has no partition yet: inserting there fails.
	insertAhead := `INSERT INTO access_denials (occurred_at, host, reason)
		VALUES (date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '2 months' + interval '1 day', 'h', 'r')`
	if err := exec(pool, insertAhead); err == nil {
		t.Fatal("insert two months ahead worked before the job ran")
	}
	rep, err := newJob(t, pool, 0).RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.PartitionsReady) != housekeeping.PartitionMonthsAhead+1 {
		t.Fatalf("partitions = %v", rep.PartitionsReady)
	}
	for m := 0; m <= housekeeping.PartitionMonthsAhead; m++ {
		name := scalar[string](t, pool, `SELECT 'access_denials_' || to_char((now() AT TIME ZONE 'UTC') + make_interval(months => $1), 'YYYY_MM')`, m)
		if scalar[bool](t, pool, `SELECT to_regclass($1) IS NOT NULL`, name) == false {
			t.Fatalf("partition %s missing", name)
		}
	}
	mustExec(t, pool, insertAhead)
	if _, err := newJob(t, pool, 0).RunOnce(ctx); err != nil {
		t.Fatalf("second run (partitions exist): %v", err)
	}
}

func TestHousekeepingIsSkippedWhileAnotherReplicaHoldsTheLock(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	seedHousekeeping(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", postgres.HousekeepingLockKey); err != nil {
		t.Fatal(err)
	}
	before := rowCounts(t, pool)
	rep, err := newJob(t, pool, 0).RunOnce(ctx)
	if err != nil || !rep.Skipped {
		t.Fatalf("RunOnce = %+v, %v; want Skipped", rep, err)
	}
	for tbl, n := range rowCounts(t, pool) {
		if n != before[tbl] {
			t.Fatalf("%s changed while the lock was held elsewhere", tbl)
		}
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", postgres.HousekeepingLockKey); err != nil {
		t.Fatal(err)
	}
	conn.Release()
	if rep, err := newJob(t, pool, 0).RunOnce(ctx); err != nil || rep.Skipped {
		t.Fatalf("after unlock: %+v, %v", rep, err)
	}
}

func TestHousekeepingConcurrentReplicasPurgeExactlyOnce(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	seedHousekeeping(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	var (
		mu    sync.Mutex
		total int64
		wg    sync.WaitGroup
	)
	for range 8 {
		wg.Go(func() {
			rep, err := newJob(t, pool, 0).RunOnce(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			total += rep.Sessions + rep.LoginCodes + rep.OAuthFlows
			mu.Unlock()
		})
	}
	wg.Wait()
	if total != 4 { // 2 sessions + 1 code + 1 flow, each deleted by exactly one replica
		t.Fatalf("rows purged across replicas = %d, want 4", total)
	}
}

func TestHousekeepingWorksUnderTheRuntimeRole(t *testing.T) {
	t.Parallel()
	admin := newTestDB(t)
	seedHousekeeping(t, admin)
	cfg := admin.Config().Copy()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE platform_runtime")
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rep, err := newJob(t, pool, 0).RunOnce(ctx) // creates the partition 2 months ahead through the SECURITY DEFINER function
	if err != nil {
		t.Fatalf("RunOnce as runtime role: %v", err)
	}
	if len(rep.PartitionsReady) != housekeeping.PartitionMonthsAhead+1 {
		t.Fatalf("partitions = %v", rep.PartitionsReady)
	}
	// The runtime role can write denials into the partition it just created and still cannot create tables.
	if _, err := pool.Exec(ctx, `INSERT INTO access_denials (occurred_at, host, reason)
		VALUES (date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' + interval '2 months' + interval '1 day', 'h', 'r')`); err != nil {
		t.Fatalf("insert into the new partition: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE hk_rogue (x int)`); err == nil {
		t.Fatal("runtime role can create tables")
	}
}

func TestHousekeepingRunLoop(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	f := seedHousekeeping(t, pool)
	job := newJob(t, pool, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { job.Run(ctx); close(done) }()

	deadline := time.Now().Add(10 * time.Second)
	for scalar[int](t, pool, `SELECT count(*) FROM sessions WHERE id IN ($1, $2)`, f.expiredSession, f.idleSession) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("loop never purged")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A row that expires later is picked up by a later tick.
	mustExec(t, pool, `INSERT INTO oauth_flows (token_hash, state, nonce, code_verifier, rd, bind_hash, expires_at)
		VALUES (sha256('late'), 's', 'n', 'v', 'https://hk.example.com/', sha256('b'), now() - interval '1 second')`)
	for scalar[int](t, pool, `SELECT count(*) FROM oauth_flows WHERE token_hash = sha256('late')`) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("loop did not pick up a later expired row")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on context cancel")
	}
}
