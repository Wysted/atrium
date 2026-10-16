package postgres

import (
	"context"

	"platform/api/internal/portal"
)

// HousekeepingLockKey exposes the housekeeping advisory lock key to the external tests.
const HousekeepingLockKey = housekeepingLockKey

// CloseBounded and ConnCloseTimeout expose the bounded close of LISTEN connections to the external tests.
var CloseBounded = closeBounded

const ConnCloseTimeout = connCloseTimeout

// WithRetry exposes the retry loop of PortalStore to the external tests.
var WithRetry = withRetry

// TxExec runs a statement inside the transaction behind tx (a Tx handed out by PortalStore.WithTx), so tests
// can provoke locking scenarios that the repositories of the portal do not need yet.
func TxExec(ctx context.Context, tx portal.Tx, sql string, args ...any) error {
	_, err := tx.(portalTx).tx.Exec(ctx, sql, args...)
	return err
}
