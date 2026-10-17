package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Wysted/atrium/platform/api/internal/portal"
)

// TokensRepo implements portal.TokenRepo. The portal role cannot read token_hash, so no query here names it
// except the insert.
type TokensRepo struct{ db dbtx }

var _ portal.TokenRepo = TokensRepo{}

// tokenColumns are the columns the portal role may select (everything but token_hash).
//
//nolint:gosec // G101: a column list, not a credential
const tokenColumns = `id::text, user_id::text, name, scope, COALESCE(created_by::text, ''), created_at, expires_at,
last_used_at, revoked_at`

func scanToken(row pgx.CollectableRow) (portal.APIToken, error) {
	var t portal.APIToken
	err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Scope, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt)
	return t, err
}

// CountLive implements portal.TokenRepo.
func (r TokensRepo) CountLive(ctx context.Context, ownerID string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT count(*) FROM api_tokens
WHERE user_id = $1::uuid AND revoked_at IS NULL AND expires_at > now()`, ownerID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count live tokens: %w", err)
	}
	return n, nil
}

// InsertToken implements portal.TokenRepo. expires_at is computed from the database clock (now() is the
// transaction start, which is also created_at's default), so the CHECK expires_at > created_at holds for any
// positive TTL.
func (r TokensRepo) InsertToken(ctx context.Context, row portal.TokenRow) (portal.APIToken, error) {
	rows, err := r.db.Query(ctx, `INSERT INTO api_tokens (user_id, name, token_hash, scope, created_by, expires_at)
VALUES ($1::uuid, $2, $3, $4, $5::uuid, now() + $6::interval) RETURNING `+tokenColumns,
		row.OwnerID, row.Name, row.Hash, row.Scope, row.CreatedBy, row.TTL)
	if err != nil {
		return portal.APIToken{}, writeError("insert token", err)
	}
	t, err := pgx.CollectOneRow(rows, scanToken)
	if err != nil {
		return portal.APIToken{}, writeError("read inserted token", err)
	}
	return t, nil
}

// GetToken implements portal.TokenRepo.
func (r TokensRepo) GetToken(ctx context.Context, id string) (portal.APIToken, error) {
	rows, err := r.db.Query(ctx, `SELECT `+tokenColumns+` FROM api_tokens WHERE id = $1::uuid`, id)
	if err != nil {
		return portal.APIToken{}, fmt.Errorf("query token: %w", err)
	}
	t, err := pgx.CollectOneRow(rows, scanToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return portal.APIToken{}, portal.ErrNotFound
	}
	if err != nil {
		return portal.APIToken{}, fmt.Errorf("read token: %w", err)
	}
	return t, nil
}

// TokensOf implements portal.TokenRepo.
func (r TokensRepo) TokensOf(ctx context.Context, ownerID string, q portal.PageQuery) ([]portal.APIToken, error) {
	rows, err := r.db.Query(ctx, `SELECT `+tokenColumns+` FROM api_tokens
WHERE user_id = $1::uuid AND ($2::uuid IS NULL OR id < $2::uuid) ORDER BY id DESC LIMIT $3`,
		ownerID, afterID(q.After), q.Limit)
	if err != nil {
		return nil, fmt.Errorf("query tokens: %w", err)
	}
	tokens, err := pgx.CollectRows(rows, scanToken)
	if err != nil {
		return nil, fmt.Errorf("read tokens: %w", err)
	}
	return tokens, nil
}

// RevokeToken implements portal.TokenRepo.
func (r TokensRepo) RevokeToken(ctx context.Context, id string) (bool, error) {
	tag, err := r.db.Exec(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE id = $1::uuid AND revoked_at IS NULL`, id)
	if err != nil {
		return false, fmt.Errorf("revoke token: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RevokeByHash implements portal.TokenRepo through revoke_api_token_by_hash (migration 0013), the only way the
// portal role touches token_hash.
func (r TokensRepo) RevokeByHash(ctx context.Context, hash []byte) (string, error) {
	var id *string
	if err := r.db.QueryRow(ctx, `SELECT revoke_api_token_by_hash($1)::text`, hash).Scan(&id); err != nil {
		return "", fmt.Errorf("revoke token by hash: %w", err)
	}
	if id == nil {
		return "", nil
	}
	return *id, nil
}
