// Package testdb provides ephemeral PostgreSQL databases for tests. Connection settings come from the PG* environment.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/postgres"
)

const timeout = 30 * time.Second

// New creates a database named platform_test_<random>, applies the migrations and drops it on cleanup.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("random name: %v", err)
	}
	name := "platform_test_" + hex.EncodeToString(suffix)

	admin, err := pgx.Connect(ctx, "") // PGDATABASE from env; used only for CREATE/DROP DATABASE
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create database: %v", err)
	}

	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), timeout)
		defer ccancel()
		c, err := pgx.Connect(cctx, "")
		if err != nil {
			t.Errorf("connect for drop: %v", err)
			return
		}
		defer c.Close(cctx)
		if _, err := c.Exec(cctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database: %v", err)
		}
	})

	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close) // runs before the drop above (LIFO)

	if _, err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
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
