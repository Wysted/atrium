// Command authz is the spike's ForwardAuth server (fake login, in-memory state).
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"spike/forwardauth/internal/portal"
)

const (
	defaultListen     = ":8080"
	defaultAuthHost   = "auth.apps.wysted.dev"
	app1Host          = "app1.apps.wysted.dev"
	app2Host          = "app2.apps.wysted.dev"
	app1              = portal.AppID("app1")
	app2              = portal.AppID("app2")
	readHeaderTimeout = 5 * time.Second
	shutdownTimeout   = 5 * time.Second
)

func main() {
	if err := run(); err != nil {
		slog.Error("authz failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	listen := os.Getenv("LISTEN_ADDR")
	if listen == "" {
		listen = defaultListen
	}
	authHost := os.Getenv("AUTH_HOST")
	if authHost == "" {
		authHost = defaultAuthHost
	}
	// Ephemeral signing key: regenerated on every start (spike only).
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	srv, err := portal.New(portal.Config{
		AuthHost: authHost,
		Apps:     map[string]portal.AppID{app1Host: app1, app2Host: app2},
		Users: map[string]portal.User{
			"alice": {ID: "6f1c1a0e-3b7e-4c1a-9d57-0a1f6e2b7c01", Roles: []string{"user"}, Apps: map[portal.AppID]bool{app1: true}},
			"bob":   {ID: "b2d8f4c3-5e19-4f8a-8a64-9c3e1d7a5b02", Roles: []string{"user", "admin"}, Apps: map[portal.AppID]bool{app1: true, app2: true}},
		},
		Key:   key,
		Clock: time.Now,
		Rand:  rand.Reader,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hs := &http.Server{Addr: listen, Handler: srv.Handler(), ReadHeaderTimeout: readHeaderTimeout}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	slog.Info("authz listening", "addr", listen, "auth_host", authHost)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
