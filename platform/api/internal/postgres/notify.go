package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// listenRetryDelay spaces LISTEN reconnect attempts.
	listenRetryDelay = 5 * time.Second
	// connCloseTimeout bounds closing a hijacked LISTEN connection at shutdown: Close writes a Terminate
	// message, which can block on a dead peer, and shutdown has a fixed budget.
	connCloseTimeout = 2 * time.Second
)

// closer is what closeBounded needs from a connection.
type closer interface {
	Close(ctx context.Context) error
}

// closeBounded closes c, giving up after timeout. It runs with a fresh context because the caller's is
// usually already canceled (that is why we are closing).
func closeBounded(ctx context.Context, c closer, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	_ = c.Close(ctx)
}

// Listen keeps a LISTEN subscription on channel alive until ctx ends, reconnecting after failures.
// onEvent runs after every (re)established subscription, because notifications may have been missed
// while disconnected, and for every notification. It must not block.
func Listen(ctx context.Context, pool *pgxpool.Pool, channel string, onEvent func(), log *slog.Logger) {
	for ctx.Err() == nil {
		if err := listenOnce(ctx, pool, channel, onEvent); err != nil && ctx.Err() == nil {
			log.Warn("postgres subscription lost", "channel", channel, "error", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(listenRetryDelay):
		}
	}
}

func listenOnce(ctx context.Context, pool *pgxpool.Pool, channel string, onEvent func()) error {
	pc, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire listener: %w", err)
	}
	conn := pc.Hijack() // a LISTENing connection must never return to the pool
	defer closeBounded(ctx, conn, connCloseTimeout)

	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	onEvent()
	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return fmt.Errorf("wait for notification: %w", err)
		}
		onEvent()
	}
}
