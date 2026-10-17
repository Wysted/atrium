package postgres_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Wysted/atrium/platform/api/internal/postgres"
	"github.com/Wysted/atrium/platform/api/internal/testdb"
)

func portalPoolFor(t *testing.T, owner *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	pool, _, err := postgres.OpenPortalPool(ctx, owner.Config())
	if err != nil {
		t.Fatalf("open portal pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func pending(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	p, err := postgres.Pending(ctx, pool)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	return p
}

// Pending works under both least-privilege roles (which cannot read schema_migrations), sees an unapplied
// migration, and ignores applied versions this binary does not know (a newer release).
func TestPending(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	roles := map[string]*pgxpool.Pool{"owner": owner, "runtime": runtimePool(t, owner), "portal": portalPoolFor(t, owner)}

	for name, pool := range roles {
		if p := pending(t, pool); len(p) != 0 {
			t.Errorf("%s: fully migrated database reports pending %v", name, p)
		}
	}

	mustExec(t, owner, `INSERT INTO schema_migrations (version) VALUES ('9999_from_the_future.sql')`)
	for name, pool := range roles {
		if p := pending(t, pool); len(p) != 0 {
			t.Errorf("%s: an unknown newer version blocks readiness: %v", name, p)
		}
	}

	last := scalar[string](t, owner, `SELECT max(version) FROM schema_migrations WHERE version < '9'`)
	mustExec(t, owner, `DELETE FROM schema_migrations WHERE version = $1`, last)
	for name, pool := range roles {
		if p := pending(t, pool); !slices.Equal(p, []string{last}) {
			t.Errorf("%s: pending = %v, want [%s]", name, p, last)
		}
	}
}

// Before 0015 the function does not exist: every migration is pending, not an error.
func TestPendingWithoutFunction(t *testing.T) {
	t.Parallel()
	pool := testdb.Empty(t)
	p := pending(t, pool)
	if len(p) < 15 || p[len(p)-1] != "0015_applied_migrations.sql" {
		t.Errorf("pending on an empty database = %v", p)
	}
}

// The function is the only schema_migrations access of the app roles: PUBLIC cannot execute it.
func TestAppliedMigrationsPrivileges(t *testing.T) {
	t.Parallel()
	pool := newTestDB(t)
	for role, want := range map[string]bool{"platform_runtime": true, "platform_portal": true} {
		if got := scalar[bool](t, pool, `SELECT has_function_privilege($1, 'applied_migrations()', 'EXECUTE')`, role); got != want {
			t.Errorf("%s EXECUTE = %v, want %v", role, got, want)
		}
	}
	if scalar[bool](t, pool, `SELECT EXISTS (SELECT 1 FROM pg_proc p, aclexplode(p.proacl) a
		WHERE p.proname = 'applied_migrations' AND a.grantee = 0)`) {
		t.Error("PUBLIC can EXECUTE applied_migrations")
	}
}
