package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/testdb"
)

const testTimeout = 30 * time.Second

// newTestDB returns an ephemeral migrated database (see testdb.New).
func newTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testdb.New(t)
}

// scalar runs a query returning one value and fails the test on error.
func scalar[T any](t *testing.T, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	var v T
	if err := pool.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return v
}

// exec runs a statement and returns its error (tests assert on it).
func exec(pool *pgxpool.Pool, sql string, args ...any) error {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	_, err := pool.Exec(ctx, sql, args...)
	return err
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if err := exec(pool, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}
