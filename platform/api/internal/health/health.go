// Package health serves the liveness and readiness probes of the internal listener.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/httpx"
	"github.com/Wysted/atrium/platform/api/internal/keys"
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

// PendingFunc lists the embedded migrations the database has not applied (postgres.Pending).
type PendingFunc func(ctx context.Context) ([]string, error)

// migrationsReason returns the readiness failure reason for the schema state, "" when every embedded migration
// is applied. Migrations newer than the binary never block.
func migrationsReason(ctx context.Context, pending PendingFunc) string {
	switch p, err := pending(ctx); {
	case err != nil:
		return "migrations check failed"
	case len(p) > 0:
		return "migrations pending"
	}
	return ""
}

// Live answers 200 while the process runs.
func Live() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(httpx.HeaderCacheControl, httpx.CacheNoStore)
		w.WriteHeader(http.StatusOK)
	})
}

// Ready answers 200 only with a reachable database, every embedded migration applied, a loaded signer and no KEK mismatch; otherwise 503.
// The body names no detail; the reason goes to the log.
func Ready(db Pinger, pending PendingFunc, signing Signing, log *slog.Logger) http.Handler {
	return readiness(log, func(ctx context.Context) string {
		if db.Ping(ctx) != nil {
			return "database unreachable"
		}
		if reason := migrationsReason(ctx, pending); reason != "" {
			return reason
		}
		if signing.KEKMismatch() {
			return "kek mismatch"
		}
		if _, err := signing.Signer(); err != nil {
			return "no signer loaded"
		}
		return ""
	})
}

// ReadyDB answers 200 only with a reachable database and every embedded migration applied; otherwise 503. It is the readiness of processes that
// hold no signing keys (the portal).
func ReadyDB(db Pinger, pending PendingFunc, log *slog.Logger) http.Handler {
	return readiness(log, func(ctx context.Context) string {
		if db.Ping(ctx) != nil {
			return "database unreachable"
		}
		return migrationsReason(ctx, pending)
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
