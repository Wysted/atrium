package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Wysted/atrium/platform/api/internal/access"
)

// DenialRepo records deny decisions in access_denials.
type DenialRepo struct {
	pool *pgxpool.Pool
}

// NewDenialRepo returns a DenialRepo over pool.
func NewDenialRepo(pool *pgxpool.Pool) *DenialRepo { return &DenialRepo{pool: pool} }

// RecordDenial inserts one row; empty optional ids are stored as NULL. The partition for the current month
// is created ahead of time by the housekeeping job.
func (r *DenialRepo) RecordDenial(ctx context.Context, d access.Denial) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO access_denials (user_id, host, app_id, initiative_id, reason, sid)
		VALUES (NULLIF($1, '')::uuid, $2, NULLIF($3, ''), NULLIF($4, '')::uuid, $5, NULLIF($6, '')::uuid)`,
		d.UserID, d.Host, d.AppID, d.InitiativeID, d.Reason, d.SessionID)
	if err != nil {
		return fmt.Errorf("record denial: %w", err)
	}
	return nil
}
