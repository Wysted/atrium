package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Wysted/atrium/platform/api/internal/audit"
	"github.com/Wysted/atrium/platform/api/internal/portal"
)

// Retry policy of PortalStore.WithTx.
const (
	// MaxTxAttempts is how many times a unit of work runs before a deadlock or serialization failure is
	// returned to the caller. Small on purpose: these errors mean heavy contention on the same few rows.
	MaxTxAttempts = 3
	// txRetryBackoff is the pause before the second attempt; attempt n waits n-1 times this.
	txRetryBackoff = 20 * time.Millisecond
	// rollbackTimeout bounds the rollback after a failed unit of work (it runs even if ctx was canceled).
	rollbackTimeout = 5 * time.Second
)

// SQLSTATE codes worth retrying: the transaction was rolled back by the server and can run again unchanged.
const (
	sqlStateDeadlockDetected     = "40P01"
	sqlStateSerializationFailure = "40001"
)

// IsRetryable reports whether err is a deadlock (40P01) or a serialization failure (40001) from PostgreSQL,
// however deeply wrapped. 40001 can surface at COMMIT, not only in a statement (pgx issue 824), so the
// classifier looks at the error, never at where it came from.
func IsRetryable(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && (pgErr.Code == sqlStateDeadlockDetected || pgErr.Code == sqlStateSerializationFailure)
}

// PortalStore implements portal.Store over the portal pool.
//
// Isolation is READ COMMITTED (the PostgreSQL default). Invariants that need serialization are enforced with
// SELECT ... FOR UPDATE on the rows involved, always locking in the same order, instead of raising the
// isolation level.
type PortalStore struct {
	pool  *pgxpool.Pool
	sleep func(ctx context.Context, d time.Duration) error
}

// NewPortalStore returns a PortalStore over pool.
func NewPortalStore(pool *pgxpool.Pool) *PortalStore {
	return &PortalStore{pool: pool, sleep: sleepContext}
}

var _ portal.Store = (*PortalStore)(nil)

// WithTx runs fn in a transaction and commits it. A deadlock or serialization failure, raised by a statement
// or by the commit, rolls the transaction back and runs fn again in a new one, up to MaxTxAttempts times; the
// retry happens outside the transaction, never inside it. Any other error, or a canceled ctx, ends it at once.
// fn must therefore be safe to run more than once and must keep its side effects inside the transaction. When
// the attempts run out the error wraps portal.ErrContention. A panic in fn rolls the transaction back and
// propagates. A commit whose context was canceled is ambiguous (the server may have committed), which is why
// mutations are idempotent by natural key (see package portal).
func (s *PortalStore) WithTx(ctx context.Context, fn func(tx portal.Tx) error) error {
	return withRetry(ctx, MaxTxAttempts, txRetryBackoff, s.sleep, func() error { return s.runOnce(ctx, fn) })
}

func (s *PortalStore) runOnce(ctx context.Context, fn func(tx portal.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		// After a successful commit this returns pgx.ErrTxClosed, which is the expected, ignorable case.
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		_ = tx.Rollback(rbCtx)
	}()
	if err := fn(portalTx{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// withRetry runs op up to attempts times while it fails with a retryable error, pausing (n-1)*backoff before
// attempt n. The last error is returned, annotated with the attempt count when the budget ran out.
func withRetry(ctx context.Context, attempts int, backoff time.Duration,
	sleep func(context.Context, time.Duration) error, op func() error,
) error {
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil || !IsRetryable(err) {
			return err
		}
		if attempt >= attempts {
			return fmt.Errorf("giving up after %d attempts: %w: %w", attempt, portal.ErrContention, err)
		}
		if serr := sleep(ctx, time.Duration(attempt)*backoff); serr != nil {
			return errors.Join(err, serr)
		}
	}
}

// sleepContext waits d or until ctx ends, returning the context error in that case.
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// portalTx exposes the repositories bound to one transaction.
type portalTx struct{ tx pgx.Tx }

var _ portal.Tx = portalTx{}

func (t portalTx) Permissions() portal.PermissionsReader { return &PermissionsRepo{db: t.tx} }
func (t portalTx) Grants() portal.GrantRepo              { return GrantsRepo{db: t.tx} }
func (t portalTx) Catalog() portal.CatalogRepo           { return CatalogRepo{db: t.tx} }
func (t portalTx) Groups() portal.GroupRepo              { return GroupsRepo{db: t.tx} }
func (t portalTx) Areas() portal.AreaRepo                { return AreasRepo{db: t.tx} }
func (t portalTx) Users() portal.UserRepo                { return UsersRepo{db: t.tx} }
func (t portalTx) Tokens() portal.TokenRepo              { return TokensRepo{db: t.tx} }
func (t portalTx) Robots() portal.RobotRepo              { return RobotsRepo{db: t.tx} }
func (t portalTx) Audit() audit.Recorder                 { return txAudit{db: t.tx} }
func (t portalTx) AuditLog() portal.AuditReader          { return AuditLogRepo{db: t.tx} }
func (t portalTx) Health() portal.HealthRepo             { return HealthRepo{db: t.tx} }
