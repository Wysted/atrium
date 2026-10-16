package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/audit"
)

// AuditRepo appends to audit_events (the runtime role has INSERT and SELECT only).
type AuditRepo struct {
	pool *pgxpool.Pool
}

// NewAuditRepo returns an AuditRepo over pool.
func NewAuditRepo(pool *pgxpool.Pool) *AuditRepo { return &AuditRepo{pool: pool} }

var _ audit.Recorder = (*AuditRepo)(nil)

// RecordAudit implements audit.Recorder; an empty ActorID or InitiativeID is stored as NULL.
func (r *AuditRepo) RecordAudit(ctx context.Context, e audit.Event) error {
	return insertAudit(ctx, r.pool, e)
}

// insertAudit appends e through a pool or a transaction.
func insertAudit(ctx context.Context, db dbtx, e audit.Event) error {
	_, err := db.Exec(ctx, `
		INSERT INTO audit_events (actor_id, initiative_id, action, target_type, target_id, before, after)
		VALUES (NULLIF($1, '')::uuid, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7)`,
		e.ActorID, e.InitiativeID, e.Action, e.TargetType, e.TargetID, e.Before, e.After)
	if err != nil {
		return fmt.Errorf("record audit event: %w", err)
	}
	return nil
}
