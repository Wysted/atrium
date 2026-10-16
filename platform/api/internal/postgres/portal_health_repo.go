package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"platform/api/internal/portal"
)

// HealthRepo implements portal.HealthRepo.
type HealthRepo struct{ db dbtx }

var _ portal.HealthRepo = HealthRepo{}

// grantCountsSQL counts the non-expired grants of an initiative and, among the manages_access ones, those that
// are effective by the definition of the last-manager rule (effectiveGrantExpr). $1 = initiative id.
const grantCountsSQL = `
SELECT count(*) FILTER (WHERE ia.expires_at IS NULL OR ia.expires_at > clock_timestamp()),
       count(*) FILTER (WHERE ir.manages_access AND ` + effectiveGrantExpr + `)
FROM initiative_access ia
JOIN initiative_roles ir ON ir.initiative_id = ia.initiative_id AND ir.id = ia.role_id
WHERE ia.initiative_id = $1::uuid`

// GrantCounts implements portal.HealthRepo.
func (r HealthRepo) GrantCounts(ctx context.Context, initiativeID string) (portal.GrantCounts, error) {
	var g portal.GrantCounts
	if err := r.db.QueryRow(ctx, grantCountsSQL, initiativeID).Scan(&g.Grants, &g.EffectiveManagers); err != nil {
		return portal.GrantCounts{}, fmt.Errorf("count grants: %w", err)
	}
	return g, nil
}

// orphanHostsSQL groups the unknown_host denials newer than $1 by host, leaving out the hosts that have a
// deployment now (a host added after being refused is no longer an orphan). Most recent denial first; $2 = limit.
const orphanHostsSQL = `
SELECT ad.host, count(*), max(ad.occurred_at)
FROM access_denials ad
WHERE ad.reason = 'unknown_host' AND ad.occurred_at >= $1
  AND NOT EXISTS (SELECT 1 FROM deployments d WHERE d.host = ad.host)
GROUP BY ad.host
ORDER BY max(ad.occurred_at) DESC, ad.host
LIMIT $2`

// OrphanHosts implements portal.HealthRepo.
func (r HealthRepo) OrphanHosts(ctx context.Context, since time.Time, limit int) ([]portal.OrphanHost, error) {
	rows, err := r.db.Query(ctx, orphanHostsSQL, since, limit)
	if err != nil {
		return nil, fmt.Errorf("query orphan hosts: %w", err)
	}
	hosts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (portal.OrphanHost, error) {
		var h portal.OrphanHost
		err := row.Scan(&h.Host, &h.Count, &h.LastDeniedAt)
		return h, err
	})
	if err != nil {
		return nil, fmt.Errorf("read orphan hosts: %w", err)
	}
	return hosts, nil
}
