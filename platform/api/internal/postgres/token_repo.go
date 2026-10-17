package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/access"
	"platform/api/internal/apitoken"
	"platform/api/internal/tokenuse"
)

// TokenRepo is the platform_runtime view of api_tokens: lookup by hash and last_used_at.
type TokenRepo struct {
	pool *pgxpool.Pool
}

// NewTokenRepo returns a TokenRepo over pool.
func NewTokenRepo(pool *pgxpool.Pool) *TokenRepo { return &TokenRepo{pool: pool} }

var _ tokenuse.Store = (*TokenRepo)(nil)

// byHashSQL reads the token and its owner live (no cache) in one indexed query. The flags carry what the
// contract states as predicates (revoked_at IS NULL AND expires_at > now(), owner active) so the log can tell the
// causes apart; the caller must treat anything for which Record.Usable is false as an invalid token.
const byHashSQL = `
SELECT t.id::text, t.scope, u.id::text, u.kind, u.email, u.name,
       t.revoked_at IS NOT NULL, t.expires_at <= now(), u.status = $2
FROM api_tokens t
JOIN users u ON u.id = t.user_id
WHERE t.token_hash = $1`

// TokenByHash returns the token with that hash, or found=false.
func (r *TokenRepo) TokenByHash(ctx context.Context, hash []byte) (rec apitoken.Record, found bool, err error) {
	err = r.pool.QueryRow(ctx, byHashSQL, hash, access.UserStatusActive).Scan(
		&rec.ID, &rec.Scope, &rec.OwnerID, &rec.OwnerKind, &rec.OwnerEmail, &rec.OwnerName,
		&rec.Revoked, &rec.Expired, &rec.OwnerActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return apitoken.Record{}, false, nil
	}
	if err != nil {
		return apitoken.Record{}, false, fmt.Errorf("look up api token: %w", err)
	}
	return rec, true, nil
}

// markUsedSQL moves last_used_at forward for the tokens in $1 (with the times in $2) in one statement and returns
// the ids it skipped. Times come from the replica that served the request, so they are capped at the database
// clock and never move the column backwards. Rows are locked in id order with SKIP LOCKED, like markSeenSQL: a
// flush never waits for a revocation or another replica. A skipped token is one that exists, whose row was locked
// by someone else; it is returned so the caller keeps it for the next tick (missing tokens, and rows that already
// hold a newer time, are not skipped: there is nothing left to do for them).
const markUsedSQL = `
WITH used AS (
    SELECT u.id, LEAST(u.at, now()) AS at
    FROM unnest($1::uuid[], $2::timestamptz[]) AS u(id, at)
), locked AS (
    SELECT t.id, t.last_used_at, used.at
    FROM api_tokens t JOIN used ON used.id = t.id
    ORDER BY t.id
    FOR NO KEY UPDATE OF t SKIP LOCKED
), written AS (
    UPDATE api_tokens t SET last_used_at = locked.at
    FROM locked
    WHERE t.id = locked.id AND (locked.last_used_at IS NULL OR locked.last_used_at < locked.at)
    RETURNING t.id
)
SELECT used.id::text FROM used
WHERE EXISTS (SELECT 1 FROM api_tokens t WHERE t.id = used.id)
  AND NOT EXISTS (SELECT 1 FROM locked WHERE locked.id = used.id)`

// MarkUsed implements tokenuse.Store.
func (r *TokenRepo) MarkUsed(ctx context.Context, used []tokenuse.Used) ([]string, error) {
	if len(used) == 0 {
		return nil, nil
	}
	ids := make([]string, len(used))
	times := make([]time.Time, len(used))
	for i, u := range used {
		ids[i], times[i] = u.ID, u.At
	}
	rows, err := r.pool.Query(ctx, markUsedSQL, ids, times)
	if err != nil {
		return nil, fmt.Errorf("mark api tokens used: %w", err)
	}
	skipped, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("mark api tokens used: %w", err)
	}
	return skipped, nil
}
