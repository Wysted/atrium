package postgres_test

import (
	"context"
	"testing"
	"time"

	"platform/api/internal/postgres"
)

// hungConn is a connection whose Close blocks until its context ends (a dead peer).
type hungConn struct {
	sawDeadline bool
}

func (c *hungConn) Close(ctx context.Context) error {
	_, c.sawDeadline = ctx.Deadline()
	<-ctx.Done()
	return ctx.Err()
}

// Shutdown must not wait forever for a LISTEN connection to close, even when the caller's context is
// already canceled (it always is at that point).
func TestCloseBoundedGivesUp(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	c := &hungConn{}
	const timeout = 100 * time.Millisecond

	done := make(chan time.Duration)
	go func() {
		start := time.Now()
		postgres.CloseBounded(canceled, c, timeout)
		done <- time.Since(start)
	}()
	select {
	case d := <-done:
		if !c.sawDeadline || d < timeout/2 || d > 10*timeout {
			t.Fatalf("closed after %s, sawDeadline=%v", d, c.sawDeadline)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closeBounded blocked on a hung Close")
	}
	if postgres.ConnCloseTimeout <= 0 || postgres.ConnCloseTimeout > 5*time.Second {
		t.Fatalf("ConnCloseTimeout = %s must be short", postgres.ConnCloseTimeout)
	}
}
