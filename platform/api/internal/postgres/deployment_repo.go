package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Wysted/atrium/platform/api/internal/lastseen"
)

// DeploymentRepo resolves hosts to apps.
type DeploymentRepo struct {
	pool *pgxpool.Pool
}

// NewDeploymentRepo returns a DeploymentRepo over pool.
func NewDeploymentRepo(pool *pgxpool.Pool) *DeploymentRepo { return &DeploymentRepo{pool: pool} }

// AppForHost returns the app id of the deployment on host (exact, lowercase), or found=false.
func (r *DeploymentRepo) AppForHost(ctx context.Context, host string) (appID string, found bool, err error) {
	err = r.pool.QueryRow(ctx, `SELECT app_id FROM deployments WHERE host = $1`, host).Scan(&appID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve host: %w", err)
	}
	return appID, true, nil
}

var _ lastseen.Store = (*DeploymentRepo)(nil)

// markSeenSQL moves last_seen_at forward for the hosts in $1 (with the times in $2) in one statement and
// returns the hosts it skipped. Times come from the replica that served the request, so they are capped at the
// database clock (a skewed replica can never write the future) and never move the column backwards. Rows are
// locked in id order with SKIP LOCKED: two replicas flushing the same hosts, or a flush racing a deployment
// removal, never wait for or deadlock with each other. A skipped host is one that exists, whose row was locked by
// someone else; it is returned so the caller keeps it for the next tick (hosts without deployment, and rows that
// already hold a newer time, are not skipped: there is nothing left to do for them). The statement changes
// neither host nor app_id, so the deployments trigger does not emit access_changed.
const markSeenSQL = `
WITH seen AS (
    SELECT s.host, LEAST(s.at, now()) AS at
    FROM unnest($1::text[], $2::timestamptz[]) AS s(host, at)
), locked AS (
    SELECT d.id, d.host, d.last_seen_at, seen.at
    FROM deployments d JOIN seen ON seen.host = d.host
    ORDER BY d.id
    FOR NO KEY UPDATE OF d SKIP LOCKED
), written AS (
    UPDATE deployments d SET last_seen_at = locked.at
    FROM locked
    WHERE d.id = locked.id AND (locked.last_seen_at IS NULL OR locked.last_seen_at < locked.at)
    RETURNING d.host
)
SELECT seen.host FROM seen
WHERE EXISTS (SELECT 1 FROM deployments d WHERE d.host = seen.host)
  AND NOT EXISTS (SELECT 1 FROM locked WHERE locked.host = seen.host)`

// MarkSeen implements lastseen.Store.
func (r *DeploymentRepo) MarkSeen(ctx context.Context, seen []lastseen.Seen) ([]string, error) {
	if len(seen) == 0 {
		return nil, nil
	}
	hosts := make([]string, len(seen))
	times := make([]time.Time, len(seen))
	for i, s := range seen {
		hosts[i], times[i] = s.Host, s.At
	}
	rows, err := r.pool.Query(ctx, markSeenSQL, hosts, times)
	if err != nil {
		return nil, fmt.Errorf("mark deployments seen: %w", err)
	}
	skipped, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("mark deployments seen: %w", err)
	}
	return skipped, nil
}
