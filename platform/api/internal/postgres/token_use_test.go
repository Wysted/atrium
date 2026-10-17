package postgres_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"platform/api/internal/postgres"
	"platform/api/internal/tokenuse"
)

func TestMarkUsed(t *testing.T) {
	t.Parallel()
	owner := newTestDB(t)
	w := world{pool: owner}
	repo := postgres.NewTokenRepo(runtimePool(t, owner))
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	tok := w.apiToken(t, w.robot(t, "bot"), "-1 hour", "1 hour", "")
	const missing = "00000000-0000-0000-0000-000000000001"
	usedAt := func() *time.Time {
		return scalar[*time.Time](t, owner, `SELECT last_used_at FROM api_tokens WHERE id = $1`, tok)
	}

	skipped, err := repo.MarkUsed(ctx, []tokenuse.Used{{ID: tok, At: time.Now().Add(-time.Minute)}, {ID: missing, At: time.Now()}})
	if err != nil || len(skipped) != 0 {
		t.Fatalf("skipped %v, err %v: a missing token is done, not skipped", skipped, err)
	}
	first := usedAt()
	if first == nil {
		t.Fatal("last_used_at not written")
	}

	// Never backwards.
	if _, err := repo.MarkUsed(ctx, []tokenuse.Used{{ID: tok, At: first.Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	if got := usedAt(); !got.Equal(*first) {
		t.Fatalf("last_used_at moved backwards to %s", got)
	}

	// A row locked by someone else is skipped, not waited for, and not written.
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM api_tokens WHERE id = $1 FOR UPDATE`, tok); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	skipped, err = repo.MarkUsed(ctx, []tokenuse.Used{{ID: tok, At: time.Now()}, {ID: missing, At: time.Now()}})
	if err != nil || !slices.Equal(skipped, []string{tok}) {
		t.Fatalf("skipped %v, err %v, want only the locked token", skipped, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("MarkUsed waited %s for a locked row", time.Since(start))
	}
	if got := usedAt(); !got.Equal(*first) {
		t.Fatal("a locked row was written")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if skipped, err = repo.MarkUsed(ctx, []tokenuse.Used{{ID: tok, At: time.Now()}}); err != nil || len(skipped) != 0 {
		t.Fatalf("retry: skipped %v, err %v", skipped, err)
	}
	if got := usedAt(); !got.After(*first) {
		t.Fatal("the retry did not write the row")
	}
}
