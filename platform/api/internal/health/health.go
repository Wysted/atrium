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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(httpx.HeaderCacheControl, httpx.CacheNoStore)
		ctx, cancel := context.WithTimeout(r.Context(), PingTimeout)
		defer cancel()
		reason := ""
		switch {
		case db.Ping(ctx) != nil:
			reason = "database unreachable"
		case signing.KEKMismatch():
			reason = "kek mismatch"
		default:
			if _, err := signing.Signer(); err != nil {
				reason = "no signer loaded"
			}
		}
		if reason != "" {
			log.Warn("not ready", "reason", reason)
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
