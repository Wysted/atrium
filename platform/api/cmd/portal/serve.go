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

	"platform/api/internal/httpx"
	"platform/api/internal/portalapp"
	"platform/api/internal/postgres"
)

// shutdownTimeout is the single budget for the whole shutdown (both listeners and the pool), kept under the
// 30 s terminationGracePeriodSeconds default of Kubernetes with margin.
const shutdownTimeout = 20 * time.Second

// serve runs the portal until ctx ends or a listener fails, then shuts down in order: listeners first
// (in-flight requests finish), the pool last.
func serve(ctx context.Context, cfg config, log *slog.Logger) error {
	// The pool refuses to start unless it runs as the least-privilege portal role (no superuser, no table
	// owner, nothing on keys or login state), otherwise none of the schema grants apply.
	pool, ident, err := postgres.NewPortalPool(ctx)
	if err != nil {
		return err
	}
	log.Info("database role verified", "current_user", ident.CurrentUser, "session_user", ident.SessionUser)
	if err := checkLoginRole(ident, cfg.AllowPrivilegedDBLogin, log); err != nil {
		pool.Close()
		return err
	}
	var shutdownCtx context.Context // set once shutdown starts
	// closePool runs once: as the last shutdown step (inside the shared budget) or, on early-return paths, from
	// the defer with a fresh budget.
	closePool := sync.OnceFunc(func() {
		c := shutdownCtx
		if c == nil {
			var cancel context.CancelFunc
			c, cancel = context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
			defer cancel()
		}
		if !waitBounded(c, pool.Close) {
			log.Error("database pool did not close within the shutdown budget")
		}
	})
	defer closePool()

	a, err := portalapp.New(portalapp.Config{
		AppID: cfg.AppID, Issuer: cfg.Issuer, JWKSURL: cfg.JWKSURL, Pool: pool, Logger: log,
	})
	if err != nil {
		return err
	}

	servers := map[string]*http.Server{
		"internal": httpx.NewServer(cfg.InternalAddr, a.Internal),
		"public":   httpx.NewServer(cfg.PublicAddr, a.Public),
	}
	failed := make(chan error, len(servers))
	for name, srv := range servers {
		go func() {
			log.Info("listening", "listener", name, "addr", srv.Addr, "app_id", cfg.AppID)
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

	var cancel context.CancelFunc
	shutdownCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	for name, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("shutdown %s listener: %w", name, err))
		}
	}
	closePool()
	return runErr
}

// checkLoginRole decides whether the login role (session_user) is safe; see the same function of
// platform-api. It returns an error for a privileged login role unless allowPrivileged (development only) is
// set; when it is set, every start logs at Error level, whether the login role is privileged or not.
func checkLoginRole(ident postgres.RuntimeIdentity, allowPrivileged bool, log *slog.Logger) error {
	problems := ident.LoginRoleProblems()
	if allowPrivileged {
		// Error level on every start, privileged login role or not: the flag must never go unnoticed.
		log.Error(envAllowPrivilegedDBLogin+" is enabled: the privileged-login-role check is off, so a privileged database login role could undo the portal role with RESET ROLE and an SQL injection would regain full privileges. Development only, never use this in production",
			"session_user", ident.SessionUser, "problems", problems)
		return nil
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to start: the database login role %q %s; it could undo the portal role with RESET ROLE. "+
		"Connect with a dedicated login role (LOGIN, not the owner of the tables, no superuser, no CREATE on public) "+
		"that is only a member of %s. For local development only, set %s=true to allow it",
		ident.SessionUser, strings.Join(problems, ", "), postgres.PortalRole, envAllowPrivilegedDBLogin)
}

// waitBounded runs wait and reports whether it finished before ctx ended.
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
