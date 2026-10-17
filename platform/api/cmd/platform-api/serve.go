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
	"platform/api/internal/googlelogin"
	"platform/api/internal/housekeeping"
	"platform/api/internal/httpx"
	"platform/api/internal/keys"
	"platform/api/internal/postgres"
	"platform/api/internal/tlsreload"
)

// shutdownTimeout is the single budget for the whole shutdown (all listeners, background jobs and the pool),
// kept under the 30 s terminationGracePeriodSeconds default of Kubernetes with margin.
const shutdownTimeout = 20 * time.Second

// serve runs platform-api until ctx ends or a listener fails, then shuts everything down in order:
// listeners first (in-flight requests finish), background jobs next, the pool last.
func serve(ctx context.Context, cfg config, log *slog.Logger) error {
	if cfg.FakeLogin && cfg.Google == nil {
		log.Warn("PLATFORM_FAKE_LOGIN is enabled: anyone who can reach the auth host can log in as any email. Never use this in production")
	}
	// A bad initial JWKS TLS pair fails start-up before anything else starts; later replacements are picked up
	// by the poll (spawned with the other background jobs).
	var jwksTLS *tlsreload.Reloader
	if t := cfg.JWKSTLS; t != nil {
		var err error
		if jwksTLS, err = tlsreload.New(t.CertFile, t.KeyFile, tlsreload.DefaultInterval, log); err != nil {
			return fmt.Errorf("JWKS TLS: %w", err)
		}
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
			c, cancel = context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
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
	acfg := app.Config{
		AuthHost: cfg.AuthHost, Issuer: cfg.Issuer, IdleTimeout: cfg.IdleTimeout, FakeLogin: cfg.FakeLogin,
		PortalAppID: cfg.PortalAppID, Pool: pool, Keyring: ring, Logger: log,
	}
	if g := cfg.Google; g != nil {
		// Issuer stays empty: googlelogin.GoogleIssuer. It is never read from the environment.
		acfg.Google = &googlelogin.Config{ClientID: g.ClientID, ClientSecret: g.ClientSecret, AllowedHD: g.AllowedHD}
	}
	a, err := app.New(acfg)
	if err != nil {
		return err
	}
	checkPortalApp(ctx, postgres.NewAccessRepository(pool), cfg.PortalAppID, log)
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
		background.Go(func() {
			fn(bgCtx)
		})
	}
	spawn(ring.Run)
	spawn(func(c context.Context) { rotator.Run(c, 0) })
	spawn(job.Run)
	spawn(a.LastSeen.Run)
	spawn(a.TokenUse.Run)
	spawn(a.RunUnknownHosts)
	if a.Login != nil {
		// Discovery retries in the background: Google being unreachable never blocks start-up or /readyz.
		spawn(a.Login.Run)
	}
	spawn(func(c context.Context) { postgres.Listen(c, pool, access.NotifyChannel, a.InvalidateAccess, log) })

	jwksSrv := httpx.NewServer(cfg.JWKSAddr, a.JWKSHandler)
	if jwksTLS != nil {
		jwksSrv.TLSConfig = jwksTLS.TLSConfig()
		spawn(jwksTLS.Run)
	} else {
		log.Warn("the JWKS listener serves plain HTTP: its integrity relies on the NetworkPolicy; set " +
			envJWKSTLSCertFile + " and " + envJWKSTLSKeyFile + " to serve HTTPS")
	}

	servers := map[string]*http.Server{
		"internal": httpx.NewServer(cfg.InternalAddr, a.Internal),
		"public":   httpx.NewServer(cfg.PublicAddr, a.Public),
		"metrics":  httpx.NewServer(cfg.MetricsAddr, a.MetricsHandler),
		"jwks":     jwksSrv,
	}
	failed := make(chan error, len(servers))
	for name, srv := range servers {
		go func() {
			log.Info("listening", "listener", name, "addr", srv.Addr)
			var err error
			if srv.TLSConfig != nil {
				err = srv.ListenAndServeTLS("", "") // certificates come from TLSConfig.GetCertificate
			} else {
				err = srv.ListenAndServe()
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
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

	// One context for all servers and the jobs: the budget is total, not per step.
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
	problems := ident.LoginRoleProblems()
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

// portalAppLookup is the read-only port used by checkPortalApp.
type portalAppLookup interface {
	PortalAppInitiativeKey(ctx context.Context, appID string) (key string, found bool, err error)
}

// portalCheckTimeout bounds the start-up sanity check of PLATFORM_PORTAL_APP_ID.
const portalCheckTimeout = 5 * time.Second

// checkPortalApp warns when PLATFORM_PORTAL_APP_ID does not point to an app of the reserved initiative. It
// never fails start-up (the database may be unavailable or the seed not applied yet); the decision itself
// is fail closed because access.Evaluator re-checks the initiative key on every decision.
func checkPortalApp(ctx context.Context, repo portalAppLookup, appID string, log *slog.Logger) {
	if appID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, portalCheckTimeout)
	defer cancel()
	key, found, err := repo.PortalAppInitiativeKey(ctx, appID)
	switch {
	case err != nil:
		log.Warn("cannot verify the portal app at start-up", "app_id", appID, "error", err.Error())
	case !found:
		log.Warn("PLATFORM_PORTAL_APP_ID does not match any app: the portal exception is inactive", "app_id", appID)
	case key != access.PlatformInitiativeKey:
		log.Warn("PLATFORM_PORTAL_APP_ID belongs to another initiative: the portal exception is inactive",
			"app_id", appID, "initiative_key", key, "required", access.PlatformInitiativeKey)
	default:
		log.Info("portal app verified", "app_id", appID)
	}
}
