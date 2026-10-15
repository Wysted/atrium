package postgres_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/audit"
	"platform/api/internal/postgres"
)

// runtimePool opens a runtime-role pool over the same test database as owner.
func runtimePool(t *testing.T, owner *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	pool, _, err := postgres.OpenRuntimePool(ctx, owner.Config())
	if err != nil {
		t.Fatalf("open runtime pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestRuntimePoolRunsAsTheRuntimeRole(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// The test connection (owner role, superuser in the compose database) must be refused.
	if _, err := postgres.CheckRuntimeRole(ctx, owner); err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("owner pool accepted as runtime: %v", err)
	}

	pool, id, err := postgres.OpenRuntimePool(ctx, owner.Config())
	if err != nil {
		t.Fatalf("OpenRuntimePool: %v", err)
	}
	defer pool.Close()
	if id.CurrentUser != postgres.RuntimeRole {
		t.Fatalf("identity = %+v", id)
	}
	// Every connection of the pool, including ones created later, has the role from the start.
	for range 3 {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var cur string
		if err := conn.QueryRow(ctx, "SELECT current_user::text").Scan(&cur); err != nil || cur != postgres.RuntimeRole {
			t.Fatalf("current_user = %q, %v", cur, err)
		}
		defer conn.Release()
	}

	// The effective role cannot touch audit_events beyond append, nor create objects.
	if err := audit.Recorder(postgres.NewAuditRepo(pool)).RecordAudit(ctx, audit.Event{
		Action: "test.event", TargetType: "test", TargetID: "1", After: map[string]string{"k": "v"},
	}); err != nil {
		t.Fatalf("runtime role cannot append to audit_events: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE audit_events SET action = 'tampered'`,
		`DELETE FROM audit_events`,
		`TRUNCATE audit_events`,
		`CREATE TABLE rogue (x int)`,
		`UPDATE signing_keys SET public_jwk = '{}'`,
		`SELECT * FROM schema_migrations`,
	} {
		if _, err := pool.Exec(ctx, stmt); pgErrCode(err) != codeInsufficientPrivs {
			t.Errorf("%q as runtime: err = %v, want insufficient_privilege", stmt, err)
		}
	}
	if n := scalar[int](t, owner, `SELECT count(*) FROM audit_events WHERE action = 'test.event'`); n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
}

// A runtime role that was granted more than the schema intends is refused at startup.
func TestCheckRuntimeRoleRefusesExtraPrivileges(t *testing.T) {
	t.Parallel()
	for name, grant := range map[string]string{
		"update on audit_events": `GRANT UPDATE ON audit_events TO platform_runtime`,
		"create in public":       `GRANT CREATE ON SCHEMA public TO platform_runtime`,
	} {
		t.Run(name, func(t *testing.T) {
			owner := newTestDB(t)
			mustExec(t, owner, grant)
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			if _, _, err := postgres.OpenRuntimePool(ctx, owner.Config()); err == nil || !strings.Contains(err.Error(), "refusing to run") {
				t.Fatalf("err = %v, want a refusal", err)
			}
		})
	}
}

// Retention (365 days) runs under the runtime role through the SECURITY DEFINER function.
func TestHousekeepingDropsExpiredDenialPartitions(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	rt := runtimePool(t, owner)

	now := time.Now().UTC()
	month := func(offset int) time.Time {
		return time.Date(now.Year(), now.Month()+time.Month(offset), 1, 0, 0, 0, 0, time.UTC)
	}
	// Partitions created by hand as the owner (the runtime function refuses months this old on purpose).
	partition := func(name string, from, to time.Time) {
		mustExec(t, owner, fmt.Sprintf(`CREATE TABLE %s PARTITION OF access_denials FOR VALUES FROM ('%s') TO ('%s')`,
			name, from.Format(time.RFC3339), to.Format(time.RFC3339)))
	}
	oldName := "access_denials_" + month(-14).Format("2006_01")
	keptName := "access_denials_" + month(-10).Format("2006_01")
	partition(oldName, month(-14), month(-13))
	partition(keptName, month(-10), month(-9))
	partition("access_denials_legacy", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))
	mustExec(t, owner, `INSERT INTO access_denials (occurred_at, host, reason) VALUES ($1, 'old', 'r'), ($2, 'kept', 'r'), (now(), 'now', 'r')`,
		month(-14).Add(time.Hour), month(-10).Add(time.Hour))

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	rep, err := newJob(t, rt, 0).RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce as runtime role: %v", err)
	}
	if len(rep.PartitionsDropped) != 1 || rep.PartitionsDropped[0] != oldName {
		t.Fatalf("dropped = %v, want [%s]", rep.PartitionsDropped, oldName)
	}
	exists := func(name string) bool {
		return scalar[bool](t, owner, `SELECT to_regclass($1) IS NOT NULL`, "public."+name)
	}
	if exists(oldName) || !exists(keptName) || !exists("access_denials_legacy") {
		t.Fatalf("old exists=%v kept exists=%v legacy exists=%v", exists(oldName), exists(keptName), exists("access_denials_legacy"))
	}
	if n := scalar[int](t, owner, `SELECT count(*) FROM access_denials WHERE host IN ('kept', 'now')`); n != 2 {
		t.Fatalf("surviving rows = %d, want 2", n)
	}
	again, err := newJob(t, rt, 0).RunOnce(ctx)
	if err != nil || len(again.PartitionsDropped) != 0 {
		t.Fatalf("second run = %+v, %v", again, err)
	}
}

func TestDropExpiredPartitionsRefusesShortRetention(t *testing.T) {
	t.Parallel()
	rt := runtimePool(t, newTestDB(t))
	for _, days := range []int{0, 1, 29, -5} {
		err := exec(rt, `SELECT drop_expired_access_denials_partitions($1)`, days)
		if pgErrCode(err) != codeInvalidParameter {
			t.Errorf("retention %d days: err = %v, want invalid_parameter_value", days, err)
		}
	}
}

// If dropping fails (here: a reader holds a lock on the old partition), the purges still commit and the
// error is reported; the next run retries.
func TestHousekeepingRetentionFailureKeepsThePurges(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	f := seedHousekeeping(t, owner)
	rt := runtimePool(t, owner)

	old := time.Date(time.Now().UTC().Year()-2, 1, 1, 0, 0, 0, 0, time.UTC)
	mustExec(t, owner, fmt.Sprintf(`CREATE TABLE access_denials_%s PARTITION OF access_denials FOR VALUES FROM ('%s') TO ('%s')`,
		old.Format("2006_01"), old.Format(time.RFC3339), old.AddDate(0, 1, 0).Format(time.RFC3339)))

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	holder, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // best effort
	if _, err := holder.Exec(ctx, fmt.Sprintf(`LOCK TABLE access_denials_%s IN ACCESS SHARE MODE`, old.Format("2006_01"))); err != nil {
		t.Fatal(err)
	}

	_, err = newJob(t, rt, 0).RunOnce(ctx)
	if err == nil || !strings.Contains(err.Error(), "drop expired partitions") {
		t.Fatalf("err = %v, want the retention failure", err)
	}
	if n := scalar[int](t, owner, `SELECT count(*) FROM sessions WHERE id IN ($1, $2)`, f.expiredSession, f.idleSession); n != 0 {
		t.Fatalf("purges rolled back with the retention failure: %d dead sessions left", n)
	}
	holder.Rollback(ctx) //nolint:errcheck // release the lock
	if rep, err := newJob(t, rt, 0).RunOnce(ctx); err != nil || len(rep.PartitionsDropped) != 1 {
		t.Fatalf("retry = %+v, %v", rep, err)
	}
}
