package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"platform/api/internal/access"
	"platform/api/internal/app"
	"platform/api/internal/housekeeping"
	"platform/api/internal/keys"
	"platform/api/internal/postgres"
)

// HTTP server limits. Requests are tiny (headers and cookies only), so these are tight.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 15 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 32 << 10 // Traefik forwards the whole Cookie header
	// shutdownTimeout is the single budget for the whole shutdown (both listeners, background jobs and the
	// pool), kept under the 30 s terminationGracePeriodSeconds default of Kubernetes with margin.
	shutdownTimeout = 20 * time.Second
)

func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// serve runs platform-api until ctx ends or a listener fails, then shuts everything down in order:
// listeners first (in-flight requests finish), background jobs next, the pool last.
func serve(ctx context.Context, cfg config, log *slog.Logger) error {
	if cfg.FakeLogin {
		log.Warn("PLATFORM_FAKE_LOGIN is enabled: anyone who can reach the auth host can log in as any email. Never use this in production")
	}
	kek, err := keys.LoadKEKFile(cfg.KEKFile)
	if err != nil {
		return fmt.Errorf("load KEK: %w", err)
	}
	// The runtime pool refuses to start unless it runs as the least-privilege role (no superuser, no table
	// owner), otherwise none of the schema grants (append-only audit_events, column-scoped signing_keys) apply.
	pool, ident, err := postgres.NewRuntimePool(ctx)
	if err != nil {
		return err
	}
	log.Info("database role verified", "current_user", ident.CurrentUser, "session_user", ident.SessionUser)
	if err := checkLoginRole(ident, cfg.AllowPrivilegedDBLogin, log); err != nil {
		pool.Close()
		return err
	}
	var shutdownCtx context.Context // set once shutdown starts; shared by every shutdown step
	// closePool runs once: explicitly as the last shutdown step (inside the shared budget) or, on early-return
	// paths, from the defer with a fresh budget. A leaked connection must not make the process outlive its
	// grace period.
	closePool := sync.OnceFunc(func() {
		c := shutdownCtx
		if c == nil {
			var cancel context.CancelFunc
			c, cancel = context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
		}
		if !waitBounded(c, pool.Close) {
			log.Error("database pool did not close within the shutdown budget")
		}
	})
	defer closePool()

	store := postgres.NewKeyStore(pool)
	ring, err := keys.NewKeyring(keys.KeyringConfig{Store: store, KEK: kek, Logger: log})
	if err != nil {
		return err
	}
	rotator, err := keys.NewRotator(keys.RotatorConfig{Store: store, KEK: kek, Logger: log})
	if err != nil {
		return err
	}
	a, err := app.New(app.Config{
		AuthHost: cfg.AuthHost, Issuer: cfg.Issuer, IdleTimeout: cfg.IdleTimeout, FakeLogin: cfg.FakeLogin,
		Pool: pool, Keyring: ring, Logger: log,
	})
	if err != nil {
		return err
	}
	job, err := housekeeping.NewJob(housekeeping.Config{
		Store: postgres.NewHousekeepingRepo(pool), IdleTimeout: a.Sessions.IdleTimeout(), Logger: log,
	})
	if err != nil {
		return err
	}

	bgCtx, stopBackground := context.WithCancel(ctx)
	defer stopBackground()
	var background sync.WaitGroup
	spawn := func(fn func(context.Context)) {
		background.Add(1)
		go func() {
			defer background.Done()
			fn(bgCtx)
		}()
	}
	spawn(ring.Run)
	spawn(func(c context.Context) { rotator.Run(c, 0) })
	spawn(job.Run)
	spawn(func(c context.Context) { postgres.Listen(c, pool, access.NotifyChannel, a.InvalidateAccess, log) })

	servers := map[string]*http.Server{
		"internal": newServer(cfg.InternalAddr, a.Internal),
		"public":   newServer(cfg.PublicAddr, a.Public),
	}
	failed := make(chan error, len(servers))
	for name, srv := range servers {
		go func() {
			log.Info("listening", "listener", name, "addr", srv.Addr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				failed <- fmt.Errorf("%s listener: %w", name, err)
			}
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case runErr = <-failed:
	}

	// One context for both servers and the jobs: the budget is total, not per step.
	var cancel context.CancelFunc
	shutdownCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	for name, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("shutdown %s listener: %w", name, err))
		}
	}
	stopBackground()
	if !waitBounded(shutdownCtx, background.Wait) {
		runErr = errors.Join(runErr, errors.New("background jobs did not stop within the shutdown budget"))
	}
	closePool()
	return runErr
}

// checkLoginRole decides whether the login role (session_user) is safe. The runtime role is assumed with a
// startup parameter, so a login role that is a superuser, bypasses RLS, owns the tables or can CREATE in
// public can undo it (RESET ROLE / SET ROLE) and an SQL injection would regain full privileges. It returns an
// error unless allowPrivileged (PLATFORM_ALLOW_PRIVILEGED_DB_LOGIN, development only) is set, in which case
// it only logs a loud WARN.
func checkLoginRole(ident postgres.RuntimeIdentity, allowPrivileged bool, log *slog.Logger) error {
	var problems []string
	if ident.SessionIsSuper {
		problems = append(problems, "is a superuser")
	}
	if ident.SessionBypassRLS {
		problems = append(problems, "bypasses row-level security")
	}
	if ident.SessionOwnsData {
		problems = append(problems, "owns (or inherits the owner of) tables in schema public")
	}
	if ident.SessionCanCreate {
		problems = append(problems, "can create objects in schema public")
	}
	if len(problems) == 0 {
		return nil
	}
	if allowPrivileged {
		log.Warn(envAllowPrivilegedDBLogin+" is enabled: the database login role is privileged, so RESET ROLE / SET ROLE undo the runtime role and an SQL injection would regain full privileges. Development only, never use this in production",
			"session_user", ident.SessionUser, "problems", problems)
		return nil
	}
	return fmt.Errorf("refusing to start: the database login role %q %s; it could undo the runtime role with RESET ROLE. "+
		"Connect with a dedicated login role (LOGIN, not the owner of the tables, no superuser, no CREATE on public) "+
		"that is only a member of %s. For local development only, set %s=true to allow it",
		ident.SessionUser, strings.Join(problems, ", "), postgres.RuntimeRole, envAllowPrivilegedDBLogin)
}

// waitBounded runs wait and reports whether it finished before ctx ended. On expiry wait keeps running in
// its goroutine; the process is about to exit anyway.
func waitBounded(ctx context.Context, wait func()) bool {
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}
