// Package testdb provides ephemeral PostgreSQL databases for tests. Connection settings come from the PG* environment.
//
// Databases are cloned from a migrated template named platform_test_tpl_<migrations hash>. Templates are never
// swept: each migration change leaves one behind (a few MB), and dropping them could break another worktree that
// still uses that migration set. Drop old ones by hand when needed.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Wysted/atrium/platform/api/internal/postgres"
)

const (
	timeout = 30 * time.Second
	// templateTimeout bounds the one-time sweep plus template build (all migrations).
	templateTimeout = 2 * time.Minute

	namePrefix     = "platform_test_"
	templatePrefix = namePrefix + "tpl_"
	// fingerprintLen is how many hex characters of the migrations hash name the template.
	fingerprintLen = 16
	// staleAfter is the age after which a leftover ephemeral database is considered abandoned. Age alone is not
	// enough (a test may run longer with a raised -timeout), so sweepStale also skips any database that still has
	// a session connected: a live database is never dropped.
	staleAfter = 15 * time.Minute
	// templateLockKey serializes template builds across test binaries (advisory locks are per database,
	// and every binary's admin connection uses the same PGDATABASE).
	templateLockKey int64 = 0x706c74706c6b // "pltplk"

	// renameRetryDelay is how long to wait for a just-closed backend to exit before ALTER DATABASE RENAME.
	renameRetryDelay = 50 * time.Millisecond
)

// ephemeralName matches databases created by this package: platform_test_<unix seconds>_<hex>.
// The creation time lives in the name because pg_database has no timestamp.
var ephemeralName = regexp.MustCompile(`^` + namePrefix + `(\d+)_[0-9a-f]+$`)

// tplState holds the per-process result of ensuring the migrated template database exists.
var tplState struct {
	once sync.Once
	name string
	err  error
}

// New creates a database named platform_test_<unix>_<random> as a copy of the migrated template and drops it
// on cleanup. The template is built once per migration set (see buildTemplate), so tests skip the migrations.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	tplState.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), templateTimeout)
		defer cancel()
		tplState.name, tplState.err = prepareTemplate(ctx)
	})
	if tplState.err != nil {
		t.Fatalf("prepare template database: %v", tplState.err)
	}
	return create(t, tplState.name)
}

// Empty is like New but returns a database with no schema, for tests that exercise the migrator itself.
func Empty(t testing.TB) *pgxpool.Pool {
	t.Helper()
	return create(t, "")
}

// create makes an ephemeral database from the given template ("" = the server default) and registers its drop.
func create(t testing.TB, from string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	name, err := newName(time.Now())
	if err != nil {
		t.Fatalf("random name: %v", err)
	}
	start := time.Now()
	admin, err := pgx.Connect(ctx, "") // PGDATABASE from env; used only for CREATE/DROP DATABASE
	if err != nil {
		t.Fatalf("%v", stepError("connect admin", name, start, err))
	}
	defer closeConn(ctx, admin)
	if err := createDatabase(ctx, admin, name, from); err != nil {
		t.Fatalf("%v", stepError("create database", name, start, err))
	}

	t.Cleanup(func() {
		dstart := time.Now()
		cctx, ccancel := context.WithTimeout(context.Background(), timeout)
		defer ccancel()
		c, err := pgx.Connect(cctx, "")
		if err != nil {
			t.Errorf("%v", stepError("connect for drop", name, dstart, err))
			return
		}
		defer closeConn(cctx, c)
		if err := dropDatabase(cctx, c, name); err != nil {
			t.Errorf("%v", stepError("drop database", name, dstart, err))
		}
	})

	pool, err := openPool(ctx, name)
	if err != nil {
		t.Fatalf("%v", stepError("open pool", name, start, err))
	}
	t.Cleanup(pool.Close) // runs before the drop above (LIFO)
	return pool
}

// prepareTemplate sweeps abandoned databases and returns the name of the migrated template, building it if needed.
func prepareTemplate(ctx context.Context) (string, error) {
	fingerprint, err := postgres.MigrationsFingerprint()
	if err != nil {
		return "", err
	}
	name := templatePrefix + fingerprint[:fingerprintLen]

	admin, err := pgx.Connect(ctx, "")
	if err != nil {
		return "", fmt.Errorf("connect admin: %w", err)
	}
	defer closeConn(ctx, admin)

	sweepStale(ctx, admin, time.Now())

	// The lock is session-scoped: closing the admin connection also releases it if this process dies.
	if _, err := admin.Exec(ctx, "SELECT pg_advisory_lock($1)", templateLockKey); err != nil {
		return "", fmt.Errorf("lock template build: %w", err)
	}
	defer func() {
		_, _ = admin.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", templateLockKey)
	}()

	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return "", fmt.Errorf("check template: %w", err)
	}
	if exists {
		return name, nil
	}
	if err := buildTemplate(ctx, admin, name); err != nil {
		return "", err
	}
	return name, nil
}

// buildTemplate migrates a scratch database and renames it to name once it is complete, so a crash mid-build
// never leaves a half-migrated database under the template name (the scratch one is swept later).
func buildTemplate(ctx context.Context, admin *pgx.Conn, name string) error {
	scratch, err := newName(time.Now())
	if err != nil {
		return fmt.Errorf("random name: %w", err)
	}
	if err := createDatabase(ctx, admin, scratch, ""); err != nil {
		return fmt.Errorf("create scratch database: %w", err)
	}
	pool, err := openPool(ctx, scratch)
	if err != nil {
		return fmt.Errorf("open scratch pool: %w", err)
	}
	if _, err := postgres.Migrate(ctx, pool); err != nil {
		pool.Close()
		return fmt.Errorf("migrate template: %w", err)
	}
	pool.Close()

	stmt := "ALTER DATABASE " + pgx.Identifier{scratch}.Sanitize() + " RENAME TO " + pgx.Identifier{name}.Sanitize()
	for {
		_, err := admin.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if err == nil || !errors.As(err, &pgErr) || pgErr.Code != pgerrcodeObjectInUse {
			return err
		}
		// The closed pool's backends may still be exiting.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(renameRetryDelay):
		}
	}
}

// pgerrcodeObjectInUse is SQLSTATE 55006 (object_in_use), raised while another session is connected to the database.
const pgerrcodeObjectInUse = "55006"

// sweepStale drops ephemeral databases older than staleAfter with no connected session, left behind by killed or
// timed-out runs. It needs a role that sees every session in pg_stat_activity (the local owner is a superuser).
// Failures are ignored: another process may be sweeping the same database, and the next run retries.
func sweepStale(ctx context.Context, admin *pgx.Conn, now time.Time) {
	rows, err := admin.Query(ctx, `SELECT d.datname FROM pg_database d
		WHERE d.datname LIKE 'platform\_test\_%' AND d.datname NOT LIKE 'platform\_test\_tpl\_%'
		AND NOT EXISTS (SELECT 1 FROM pg_stat_activity a WHERE a.datname = d.datname)`)
	if err != nil {
		return
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return
	}
	for _, name := range names {
		if isStale(name, now) {
			_ = dropDatabase(ctx, admin, name)
		}
	}
}

// isStale reports whether name is an ephemeral database created more than staleAfter before now.
// Templates and names from older versions of this package do not match and are never swept.
func isStale(name string, now time.Time) bool {
	m := ephemeralName.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	sec, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return false
	}
	return now.Sub(time.Unix(sec, 0)) > staleAfter
}

// newName returns platform_test_<unix seconds>_<16 hex>.
func newName(now time.Time) (string, error) {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d_%s", namePrefix, now.Unix(), hex.EncodeToString(suffix)), nil
}

// createDatabase creates name from the given template ("" = the server default).
func createDatabase(ctx context.Context, c *pgx.Conn, name, from string) error {
	stmt := "CREATE DATABASE " + pgx.Identifier{name}.Sanitize()
	if from != "" {
		// WAL_LOG (the PG15+ default, stated explicitly) copies the template through WAL. FILE_COPY would force
		// a checkpoint before and after each copy, stalling parallel packages on I/O for no gain on a small template.
		stmt += " TEMPLATE " + pgx.Identifier{from}.Sanitize() + " STRATEGY WAL_LOG"
	}
	_, err := c.Exec(ctx, stmt)
	return err
}

func dropDatabase(ctx context.Context, c *pgx.Conn, name string) error {
	_, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	return err
}

// maxTestConns caps every test pool: pgxpool defaults to NumCPU connections, and with one database (three pools)
// per test, packages in parallel and 26-goroutine race tests that exhausted the server's max_connections (100,
// "too many clients already"). Eight still lets several transactions run at once. The role pools (Runtime,
// Portal) clone the owner's config, so they inherit the cap.
const maxTestConns = 8

func openPool(ctx context.Context, database string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.Database = database
	cfg.MaxConns = maxTestConns
	return pgxpool.NewWithConfig(ctx, cfg)
}

// Runtime opens a second pool over the same database as owner whose connections run as the least-privilege
// platform_runtime role, exactly as "platform-api serve" does. Fixtures keep using owner; the code under
// test must use the returned pool so a missing grant fails the test.
func Runtime(t testing.TB, owner *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	pool, _, err := postgres.OpenRuntimePool(ctx, owner.Config())
	if err != nil {
		t.Fatalf("open runtime pool: %v", err)
	}
	t.Cleanup(pool.Close) // registered after owner's cleanups, so it runs before the DROP DATABASE
	return pool
}

// Portal opens a pool over the same database as owner whose connections run as the least-privilege
// platform_portal role, exactly as "portal serve" does. Fixtures keep using owner; the code under test must use
// the returned pool so a missing grant fails the test.
func Portal(t testing.TB, owner *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	pool, _, err := postgres.OpenPortalPool(ctx, owner.Config())
	if err != nil {
		t.Fatalf("open portal pool: %v", err)
	}
	t.Cleanup(pool.Close) // registered after owner's cleanups, so it runs before the DROP DATABASE
	return pool
}

// stepError names the failing step, the database and how long the step ran: a bare "context deadline exceeded"
// does not say where the time went (DROP DATABASE waits for an immediate checkpoint, see the testdb notes in go.md).
func stepError(step, database string, start time.Time, err error) error {
	return fmt.Errorf("%s %s (after %s): %w", step, database, time.Since(start).Round(time.Millisecond), err)
}

// closeConn closes an admin connection; a failed close of a short-lived test connection carries no information.
func closeConn(ctx context.Context, c *pgx.Conn) {
	_ = c.Close(ctx)
}
