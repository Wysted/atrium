package portal

import (
	"context"
	"fmt"

	"github.com/Wysted/atrium/platform/api/internal/adminz"
)

// ListAudit lists audit events newest first (keyset on occurred_at and id). Platform admins see everything, or
// one initiative with the filter; a manager sees only the events of their initiatives, and asking for another
// initiative is ErrNotFound. The scope that limits the rows comes from adminz.Decide.
func (s *Service) ListAudit(ctx context.Context, callerID string, q AuditQuery) (Page[AuditEvent], error) {
	q.InitiativeID = lowerIfUUID(q.InitiativeID)
	size := pageSize(q.Page.Limit)
	var out Page[AuditEvent]
	err := s.store.WithTx(ctx, func(tx Tx) error {
		out = Page[AuditEvent]{}
		_, d, err := begin(ctx, tx, callerID, adminz.AuditList, adminz.Resource{Type: "audit", InitiativeID: q.InitiativeID})
		if err != nil {
			return err
		}
		if q.InitiativeID != "" && !IsUUID(q.InitiativeID) {
			return invalid("initiative_id", CodeInvalid)
		}
		after, err := decodeCursor(cursorKindAudit, q.Page.Cursor, true)
		if err != nil {
			return err
		}
		rows, err := tx.AuditLog().ListAudit(ctx, d.Scope, PageQuery{After: after, Limit: size + 1})
		if err != nil {
			return fmt.Errorf("list audit: %w", err)
		}
		out = pageOf(rows, size, cursorKindAudit, func(e AuditEvent) Cursor { return Cursor{At: e.OccurredAt, ID: e.ID} })
		return nil
	})
	return out, err
}
