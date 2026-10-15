package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/housekeeping"
)

// housekeepingLockKey serializes housekeeping across replicas.
const housekeepingLockKey int64 = 0x706c617468736b70 // "plathskp"

// HousekeepingRepo implements housekeeping.Store.
type HousekeepingRepo struct {
	pool *pgxpool.Pool
}

// NewHousekeepingRepo returns a HousekeepingRepo over pool.
func NewHousekeepingRepo(pool *pgxpool.Pool) *HousekeepingRepo { return &HousekeepingRepo{pool: pool} }

var _ housekeeping.Store = (*HousekeepingRepo)(nil)

// Housekeep purges expired rows and ensures the access_denials partitions, in one transaction guarded by a
// try-advisory lock. Expiry always uses the database clock. Sessions are purged when past their absolute
// expiry or idle for longer than p.IdleTimeout; their app sessions and login codes go by cascade, and
// expired app sessions and codes of live sessions are purged on their own.
func (r *HousekeepingRepo) Housekeep(ctx context.Context, p housekeeping.Params) (housekeeping.Report, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return housekeeping.Report{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var got bool
	if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", housekeepingLockKey).Scan(&got); err != nil {
		return housekeeping.Report{}, fmt.Errorf("try advisory lock: %w", err)
	}
	if !got {
		return housekeeping.Report{Skipped: true}, nil
	}

	var rep housekeeping.Report
	purges := []struct {
		dst  *int64
		sql  string
		args []any
	}{
		{&rep.OAuthFlows, `DELETE FROM oauth_flows WHERE expires_at <= now()`, nil},
		{&rep.LoginCodes, `DELETE FROM login_codes WHERE expires_at <= now()`, nil},
		{&rep.AppSessions, `DELETE FROM app_sessions WHERE expires_at <= now()`, nil},
		{&rep.Sessions, `DELETE FROM sessions WHERE expires_at <= now() OR last_seen_at <= now() - make_interval(secs => $1)`,
			[]any{p.IdleTimeout.Seconds()}},
	}
	for _, d := range purges {
		tag, err := tx.Exec(ctx, d.sql, d.args...)
		if err != nil {
			return housekeeping.Report{}, fmt.Errorf("purge: %w", err)
		}
		*d.dst = tag.RowsAffected()
	}

	rows, err := tx.Query(ctx, `
		SELECT create_access_denials_partition(((now() AT TIME ZONE 'UTC')::date + make_interval(months => m))::date)
		FROM generate_series(0, $1::int) AS m ORDER BY m`, p.PartitionMonthsAhead)
	if err != nil {
		return housekeeping.Report{}, fmt.Errorf("create partitions: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return housekeeping.Report{}, fmt.Errorf("scan partition: %w", err)
		}
		rep.PartitionsReady = append(rep.PartitionsReady, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return housekeeping.Report{}, fmt.Errorf("create partitions: %w", err)
	}

	// Retention runs in a savepoint: it takes a short ACCESS EXCLUSIVE lock on access_denials (bounded by
	// lock_timeout inside the function). If it fails, the purges above still commit and the error is
	// returned after the commit; the next run retries.
	dropped, dropErr := dropExpiredPartitions(ctx, tx, p.DenialsRetentionDays)
	rep.PartitionsDropped = dropped

	if err := tx.Commit(ctx); err != nil {
		return housekeeping.Report{}, fmt.Errorf("commit: %w", err)
	}
	return rep, dropErr
}

func dropExpiredPartitions(ctx context.Context, tx pgx.Tx, retentionDays int) (dropped []string, err error) {
	if retentionDays <= 0 {
		return nil, nil
	}
	sp, err := tx.Begin(ctx) // savepoint
	if err != nil {
		return nil, fmt.Errorf("begin retention: %w", err)
	}
	rows, err := sp.Query(ctx, `SELECT drop_expired_access_denials_partitions($1)`, retentionDays)
	if err == nil {
		dropped, err = pgx.CollectRows(rows, pgx.RowTo[string])
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("drop expired partitions: %w", err), sp.Rollback(context.WithoutCancel(ctx)))
	}
	if err := sp.Commit(ctx); err != nil {
		return nil, fmt.Errorf("release retention savepoint: %w", err)
	}
	return dropped, nil
}
