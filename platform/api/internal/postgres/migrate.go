// Package postgres is the pgx v5 adapter: connection pool, embedded migrations and repositories.
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const (
	migrationsDir = "migrations"
	// migrationsLockKey is the advisory lock key serializing concurrent migrators.
	migrationsLockKey int64 = 0x706c6174666d6967 // "platfmig"
)

// NewPool builds a pool from the standard PG* environment variables (PGHOST, PGPASSFILE, ...).
func NewPool(ctx context.Context) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, fmt.Errorf("parse pg config: %w", err)
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

// Migrate applies pending embedded migrations in filename order, each in its own transaction.
// It returns the versions applied by this call. Concurrent callers are serialized by an advisory lock.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	entries, err := fs.ReadDir(migrationsFS, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationsLockKey); err != nil {
		return nil, fmt.Errorf("lock migrations: %w", err)
	}
	defer func() {
		// Best effort with a fresh context: ctx may already be cancelled.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationsLockKey)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text        PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	var applied []string
	for _, name := range names {
		var exists bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", name).Scan(&exists); err != nil {
			return applied, fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists {
			continue
		}
		body, err := migrationsFS.ReadFile(path.Join(migrationsDir, name))
		if err != nil {
			return applied, fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := applyOne(ctx, conn, name, string(body)); err != nil {
			return applied, err
		}
		applied = append(applied, name)
	}
	return applied, nil
}

type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

func applyOne(ctx context.Context, db txBeginner, name, body string) (err error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback(context.WithoutCancel(ctx)))
		}
	}()
	// Simple protocol allows several statements per Exec.
	if _, err = tx.Exec(ctx, body, pgx.QueryExecModeSimpleProtocol); err != nil {
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", name); err != nil {
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}
