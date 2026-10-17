package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Wysted/atrium/platform/api/internal/googlelogin"
)

// FlowRepo implements googlelogin.FlowStore on oauth_flows. Expiry uses the database clock.
type FlowRepo struct {
	pool *pgxpool.Pool
}

// NewFlowRepo returns a FlowRepo over pool.
func NewFlowRepo(pool *pgxpool.Pool) *FlowRepo { return &FlowRepo{pool: pool} }

var _ googlelogin.FlowStore = (*FlowRepo)(nil)

// InsertFlow implements googlelogin.FlowStore.
func (r *FlowRepo) InsertFlow(ctx context.Context, f googlelogin.NewFlow) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO oauth_flows (token_hash, state, nonce, code_verifier, rd, bind_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, now() + make_interval(secs => $7))`,
		f.TokenHash, f.State, f.Nonce, f.Verifier, f.RD, f.BindHash, seconds(f.TTL))
	if err != nil {
		return fmt.Errorf("insert oauth flow: %w", err)
	}
	return nil
}

// ConsumeFlow implements googlelogin.FlowStore: a single UPDATE decides, so exactly one caller wins. The state
// is part of the WHERE clause, so a caller that does not know it burns nothing. The equality is not constant
// time; that is acceptable because the state is 256 random bits issued per flow, and the row is addressed by
// the hash of a second 256-bit secret (the cookie), so there is no useful guess to refine.
func (r *FlowRepo) ConsumeFlow(ctx context.Context, tokenHash []byte, state string) (googlelogin.Flow, error) {
	var f googlelogin.Flow
	err := r.pool.QueryRow(ctx, `
		UPDATE oauth_flows SET consumed_at = now()
		WHERE token_hash = $1 AND state = $2 AND consumed_at IS NULL AND expires_at > now()
		RETURNING state, nonce, code_verifier, rd, bind_hash`, tokenHash, state,
	).Scan(&f.State, &f.Nonce, &f.Verifier, &f.RD, &f.BindHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return googlelogin.Flow{}, googlelogin.ErrInvalidFlow
	}
	if err != nil {
		return googlelogin.Flow{}, fmt.Errorf("consume oauth flow: %w", err)
	}
	return f, nil
}
