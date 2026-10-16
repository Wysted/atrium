package tlsreload_test

import (
	"context"
	"testing"
	"time"
)

const waitTimeout = 5 * time.Second

// stopCtx returns a context canceled when stop closes.
func stopCtx(stop <-chan struct{}) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-stop; cancel() }()
	return ctx
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
