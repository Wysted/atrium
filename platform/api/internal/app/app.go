// Package app assembles the two HTTP surfaces of platform-api from their dependencies. The binary and the
// integration tests both build it here, so what is tested is what runs.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"platform/api/internal/access"
	"platform/api/internal/assertion"
	"platform/api/internal/authhost"
	"platform/api/internal/forwardauth"
	"platform/api/internal/googlelogin"
	"platform/api/internal/health"
	"platform/api/internal/httpx"
	"platform/api/internal/jwks"
	"platform/api/internal/keys"
	"platform/api/internal/lastseen"
	"platform/api/internal/metrics"
	"platform/api/internal/postgres"
	"platform/api/internal/ratelimit"
	"platform/api/internal/session"
	"platform/api/internal/tokenuse"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Config describes one platform-api instance.
type Config struct {
	AuthHost    string // lowercase host of the auth host, e.g. auth.apps.example.com
	Issuer      string // exact JWT iss, https://<AuthHost>
	IdleTimeout time.Duration
	// Google enables Google login (and switches FakeLogin off). Flows, RedirectURL and Logger are filled in here;
	// Issuer is empty in production (GoogleIssuer) and set only by tests.
	Google    *googlelogin.Config
	FakeLogin bool
	// PortalAppID is the id of the portal app (PLATFORM_PORTAL_APP_ID, block 6); empty means there is none.
	PortalAppID string
	Pool        *pgxpool.Pool
	Keyring     *keys.Keyring
	CacheTTL    time.Duration // decision cache TTL; zero means the contract default
	// RequestTimeout is the per-request deadline of ForwardAuth and the auth host; zero means httpx.RequestTimeout.
	RequestTimeout time.Duration
	Logger         *slog.Logger
}

// App holds the handlers of both listeners and the hooks the binary wires to background work.
type App struct {
	// Internal serves /forwardauth and the probes (Traefik and kubelet only; no JWKS).
	Internal http.Handler
	// JWKSHandler serves the JWKS alone on its own listener (PLATFORM_JWKS_ADDR): the NetworkPolicy filters by port,
	// not by path, so app namespaces get a port that cannot reach /forwardauth or /readyz.
	JWKSHandler http.Handler
	// Public serves the auth host browser routes through the IngressRoute.
	Public http.Handler
	// Sessions is exposed for the housekeeping job (idle timeout) and tests.
	Sessions *session.Service
	// Login is the Google login, nil when it is not configured. The caller must run Login.Run in the background:
	// until the provider discovery succeeds /login answers 503, and nothing else (/readyz, /forwardauth) waits for it.
	Login *googlelogin.Login
	// InvalidateAccess clears the decision cache; call it on NOTIFY access_changed.
	InvalidateAccess func()
	// LastSeen aggregates the authorized requests per deployment host. The caller must run LastSeen.Run in the
	// background: it flushes last_seen_at once per minute and once more when its context ends.
	LastSeen *lastseen.Recorder
	// TokenUse aggregates the tokens that authorized a request. The caller must run TokenUse.Run in the background:
	// it flushes last_used_at once per minute and once more when its context ends.
	TokenUse *tokenuse.Recorder
	// MetricsHandler serves /metrics on its own listener (PLATFORM_METRICS_ADDR): the NetworkPolicy filters by port,
	// not by path, so the metrics cannot share a listener that Traefik or the app namespaces reach.
	MetricsHandler http.Handler
	// Metrics is the registry behind /metrics: one per App, never the global default.
	Metrics *prometheus.Registry
	// RunUnknownHosts writes the queued unknown_host denials (forwardauth.Handler.Run). Run it in the background.
	RunUnknownHosts func(ctx context.Context)
}

// New builds the app.
func New(cfg Config) (*App, error) {
	if cfg.Pool == nil || cfg.Keyring == nil || cfg.AuthHost == "" || cfg.Issuer == "" {
		return nil, errors.New("app: pool, keyring, auth host and issuer are required")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	repo := postgres.NewSessionRepo(cfg.Pool)
	sessions, err := session.NewService(session.Config{
		Sessions: repo, AppSessions: repo, Codes: repo, Hosts: repo, IdleTimeout: cfg.IdleTimeout,
	})
	if err != nil {
		return nil, err
	}
	issuer, err := assertion.NewIssuer(assertion.Config{Issuer: cfg.Issuer, Signers: cfg.Keyring})
	if err != nil {
		return nil, err
	}
	deployments := postgres.NewDeploymentRepo(cfg.Pool)
	users := postgres.NewUserRepo(cfg.Pool)
	auditLog := postgres.NewAuditRepo(cfg.Pool)

	reg, err := metrics.NewRegistry()
	if err != nil {
		return nil, err
	}
	pm, err := metrics.NewPlatform(reg)
	if err != nil {
		return nil, err
	}

	seen, err := lastseen.New(lastseen.Config{Store: deployments, Observer: pm, Logger: log})
	if err != nil {
		return nil, err
	}
	tokens := postgres.NewTokenRepo(cfg.Pool)
	tokenUse, err := tokenuse.New(tokenuse.Config{Store: tokens, Logger: log})
	if err != nil {
		return nil, err
	}
	fa, err := forwardauth.New(forwardauth.Config{
		AuthHost:       cfg.AuthHost,
		Seen:           seen,
		Tokens:         tokens,
		TokenUse:       tokenUse,
		Observer:       pm,
		Apps:           deployments,
		Sessions:       sessions,
		Evaluator:      access.NewEvaluator(postgres.NewAccessRepository(cfg.Pool), access.WithPortalApp(cfg.PortalAppID)),
		Profiles:       users,
		Denials:        postgres.NewDenialRepo(cfg.Pool),
		Issuer:         issuer,
		Audit:          auditLog,
		CacheTTL:       cfg.CacheTTL,
		RequestTimeout: cfg.RequestTimeout,
		Logger:         log,
	})
	if err != nil {
		return nil, err
	}
	var login *googlelogin.Login
	var loginIface authhost.Login // stays a nil interface when Google is off
	if cfg.Google != nil {
		gc := *cfg.Google
		gc.Flows = postgres.NewFlowRepo(cfg.Pool)
		gc.RedirectURL = "https://" + cfg.AuthHost + authhost.PathCallback
		gc.Logger = log
		if login, err = googlelogin.New(gc); err != nil {
			return nil, err
		}
		loginIface = login
	}
	ah, err := authhost.New(authhost.Config{
		AuthHost: cfg.AuthHost, Sessions: sessions, Apps: deployments, Users: users, Login: loginIface,
		// The limiter lives in this process: with several replicas the effective limit is per replica.
		Limiter:        ratelimit.New(ratelimit.Config{}),
		Audit:          auditLog,
		FakeLogin:      cfg.FakeLogin,
		Observer:       pm,
		RequestTimeout: cfg.RequestTimeout,
		Logger:         log,
	})
	if err != nil {
		return nil, err
	}

	err = metrics.RegisterPlatformState(reg, metrics.PlatformSources{
		Pool: cfg.Pool, Keys: cfg.Keyring, Pending: seen.Pending, UnknownDrops: fa.UnknownHostDrops,
		TokenPending: tokenUse.Pending, TokenDrops: tokenUse.Dropped,
	})
	if err != nil {
		return nil, err
	}

	internal := http.NewServeMux()
	internal.Handle("/forwardauth", fa) // Traefik always uses GET, but any method must reach the decision code
	internal.Handle("GET "+health.PathLive, health.Live())
	internal.Handle("GET "+health.PathReady, health.Ready(cfg.Pool, cfg.Keyring, log))
	jwksMux := http.NewServeMux()
	jwksMux.Handle(jwks.Route, pm.CountJWKS(metrics.ListenerJWKS, jwks.Handler(cfg.Keyring)))
	mux := http.NewServeMux()
	mux.Handle(metrics.Route, metrics.Handler(reg))

	public := http.NewServeMux()
	ah.Register(public)
	public.Handle(jwks.Route, pm.CountJWKS(metrics.ListenerPublic, jwks.Handler(cfg.Keyring)))

	return &App{
		Internal:         httpx.Recover(log, internal),
		Public:           httpx.Recover(log, public),
		JWKSHandler:      httpx.Recover(log, jwksMux),
		Sessions:         sessions,
		Login:            login,
		InvalidateAccess: fa.Invalidate,
		LastSeen:         seen,
		TokenUse:         tokenUse,
		MetricsHandler:   httpx.Recover(log, mux),
		Metrics:          reg,
		RunUnknownHosts:  fa.Run,
	}, nil
}
