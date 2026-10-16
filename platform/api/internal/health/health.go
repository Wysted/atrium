// Package health serves the liveness and readiness probes of the internal listener.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"platform/api/internal/httpx"
	"platform/api/internal/keys"
)

const (
	PathLive  = "/healthz"
	PathReady = "/readyz"

	// PingTimeout bounds the database check of /readyz.
	PingTimeout = 2 * time.Second
)

// Pinger checks the database connection.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Signing reports the state of the signing keys (keys.Keyring).
type Signing interface {
	Signer() (keys.Signer, error)
	KEKMismatch() bool
}

// Live answers 200 while the process runs.
func Live() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(httpx.HeaderCacheControl, httpx.CacheNoStore)
		w.WriteHeader(http.StatusOK)
	})
}

// Ready answers 200 only with a reachable database, a loaded signer and no KEK mismatch; otherwise 503.
// The body names no detail; the reason goes to the log.
func Ready(db Pinger, signing Signing, log *slog.Logger) http.Handler {
	return readiness(log, func(ctx context.Context) string {
		switch {
		case db.Ping(ctx) != nil:
			return "database unreachable"
		case signing.KEKMismatch():
			return "kek mismatch"
		}
		if _, err := signing.Signer(); err != nil {
			return "no signer loaded"
		}
		return ""
	})
}

// ReadyDB answers 200 only with a reachable database; otherwise 503. It is the readiness of processes that
// hold no signing keys (the portal).
func ReadyDB(db Pinger, log *slog.Logger) http.Handler {
	return readiness(log, func(ctx context.Context) string {
		if db.Ping(ctx) != nil {
			return "database unreachable"
		}
		return ""
	})
}

// readiness turns a check that returns the reason it failed ("" when ready) into a probe handler.
func readiness(log *slog.Logger, check func(ctx context.Context) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(httpx.HeaderCacheControl, httpx.CacheNoStore)
		ctx, cancel := context.WithTimeout(r.Context(), PingTimeout)
		defer cancel()
		if reason := check(ctx); reason != "" {
			log.Warn("not ready", "reason", reason)
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
