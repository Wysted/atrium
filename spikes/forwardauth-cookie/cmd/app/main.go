// Command app is a demo app that trusts only a valid X-Portal-Token.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"spike/forwardauth/internal/appauth"
	"spike/forwardauth/internal/token"
)

const (
	defaultListen     = ":8080"
	jwksAttempts      = 30
	jwksRetryDelay    = 2 * time.Second
	jwksClientTimeout = 5 * time.Second
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 5 * time.Second
)

func main() {
	if err := run(); err != nil {
		slog.Error("app failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	appID := os.Getenv("APP_ID")
	jwksURL := os.Getenv("AUTHZ_JWKS_URL")
	if appID == "" || jwksURL == "" {
		return errors.New("APP_ID and AUTHZ_JWKS_URL are required")
	}
	listen := os.Getenv("LISTEN_ADDR")
	if listen == "" {
		listen = defaultListen
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	set, err := token.Fetch(ctx, &http.Client{Timeout: jwksClientTimeout}, jwksURL, jwksAttempts, jwksRetryDelay)
	if err != nil {
		return err
	}
	v, err := appauth.NewVerifier(appID, set, time.Now)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/", v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := appauth.ClaimsFrom(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"app": appID, "sub": c.Subject, "roles": c.Roles})
	})))

	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("app listening", "app", appID, "addr", listen)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
