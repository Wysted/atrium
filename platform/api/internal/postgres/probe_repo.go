package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/probe"
)

// probeLockKey serializes probe rounds across portal replicas ("platprob").
const probeLockKey int64 = 0x706c617470726f62

// probeUnlockTimeout bounds releasing the advisory lock (it runs even when the round's context ended).
const probeUnlockTimeout = 5 * time.Second

// ProbeRepo implements probe.Store over the portal pool.
type ProbeRepo struct {
	pool *pgxpool.Pool
}

// NewProbeRepo returns a ProbeRepo over pool.
func NewProbeRepo(pool *pgxpool.Pool) *ProbeRepo { return &ProbeRepo{pool: pool} }

var _ probe.Store = (*ProbeRepo)(nil)

// dueTargetsSQL lists the deployments never probed or last probed more than $1 seconds ago (database clock).
const dueTargetsSQL = `
SELECT host, app_id FROM deployments
WHERE last_probe_at IS NULL OR last_probe_at < now() - make_interval(secs => $1)
ORDER BY host`

// controlTargetsSQL lists the deployments of the control app ($1), due or not.
const controlTargetsSQL = `SELECT host, app_id FROM deployments WHERE app_id = $1 ORDER BY host`

type targetQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func queryTargets(ctx context.Context, q targetQuerier, sql string, args ...any) ([]probe.Target, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	targets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (probe.Target, error) {
		var t probe.Target
		err := row.Scan(&t.Host, &t.AppID)
		return t, err
	})
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return targets, nil
}

// storeProbeSQL writes the results ($1 = hosts, $2 = statuses) in one statement. Rows are locked in id order
// with SKIP LOCKED, like the last_seen_at flush: a row locked by someone else or removed meanwhile keeps its old
// probe time and is probed again on the next round. Only telemetry columns change, so the deployments trigger
// does not emit access_changed.
const storeProbeSQL = `
WITH probed AS (
    SELECT * FROM unnest($1::text[], $2::text[]) AS p(host, status)
), todo AS (
    SELECT d.id, probed.status
    FROM deployments d JOIN probed ON probed.host = d.host
    ORDER BY d.id
    FOR NO KEY UPDATE OF d SKIP LOCKED
)
UPDATE deployments d SET last_probe_at = now(), last_probe_status = todo.status FROM todo WHERE d.id = todo.id`

// statusCountsSQL counts deployments by last probe status; those never probed are reported as 'never'.
const statusCountsSQL = `SELECT coalesce(last_probe_status, $1), count(*) FROM deployments GROUP BY 1`

var _ probe.StatusCounter = (*ProbeRepo)(nil)

// StatusCounts implements probe.StatusCounter.
func (r *ProbeRepo) StatusCounts(ctx context.Context) (map[probe.Status]int, error) {
	rows, err := r.pool.Query(ctx, statusCountsSQL, string(probe.StatusNever))
	if err != nil {
		return nil, fmt.Errorf("count deployments by probe status: %w", err)
	}
	defer rows.Close()
	counts := map[probe.Status]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("count deployments by probe status: %w", err)
		}
		counts[probe.Status(status)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count deployments by probe status: %w", err)
	}
	return counts, nil
}

// Round implements probe.Store. The lock is a session-level advisory lock on a dedicated connection, held while
// the (slow) HTTP probes run: unlike a transaction-level lock it leaves no transaction open for minutes, and it
// is released by the database if the connection dies.
func (r *ProbeRepo) Round(ctx context.Context, dueAfter time.Duration, controlAppID string,
	run func(ctx context.Context, due, control []probe.Target) []probe.Result,
) (probe.Report, error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return probe.Report{}, fmt.Errorf("acquire connection: %w", err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			conn.Release()
		}
	}
	defer release()

	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", probeLockKey).Scan(&got); err != nil {
		return probe.Report{}, fmt.Errorf("try advisory lock: %w", err)
	}
	if !got {
		return probe.Report{Skipped: true}, nil
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeUnlockTimeout)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRow(uctx, "SELECT pg_advisory_unlock($1)", probeLockKey).Scan(&unlocked); err != nil || !unlocked {
			// The lock may still be held by this session: never hand the connection back to the pool.
			released = true
			_ = conn.Hijack().Close(uctx)
		}
	}()

	targets, err := queryTargets(ctx, conn, dueTargetsSQL, dueAfter.Seconds())
	if err != nil {
		return probe.Report{}, fmt.Errorf("due deployments: %w", err)
	}
	if len(targets) == 0 {
		return probe.Report{}, nil
	}
	control, err := queryTargets(ctx, conn, controlTargetsSQL, controlAppID)
	if err != nil {
		return probe.Report{}, fmt.Errorf("control deployments: %w", err)
	}

	results := run(ctx, targets, control)
	if len(results) == 0 {
		return probe.Report{}, nil
	}
	hosts := make([]string, len(results))
	statuses := make([]string, len(results))
	for i, res := range results {
		hosts[i], statuses[i] = res.Host, string(res.Status)
	}
	// Results are stored even if ctx just ended: they are already measured. The write is bounded on its own.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeUnlockTimeout)
	defer cancel()
	tag, err := conn.Exec(wctx, storeProbeSQL, hosts, statuses)
	if err != nil {
		return probe.Report{}, fmt.Errorf("store probe results: %w", err)
	}
	return probe.Report{Stored: tag.RowsAffected()}, nil
}
