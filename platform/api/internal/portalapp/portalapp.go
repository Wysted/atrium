// Package portalapp assembles the two HTTP surfaces of the portal process from their dependencies. The binary
// and the integration tests both build it here, so what is tested is what runs.
package portalapp

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"platform/api/internal/health"
	"platform/api/internal/httpx"
	"platform/api/internal/metrics"
	"platform/api/internal/portal"
	"platform/api/internal/portalhttp"
	"platform/api/internal/postgres"
	"platform/api/internal/probe"
	platformauth "platform/sdk/go"
)

// ExemptPath is the health path handed to the SDK in place of its default "/healthz". The SDK lets that one path
// through without validating an assertion (kubelet probes do not go through Traefik); the portal's probes live
// on the internal listener instead, so the public listener must have no unauthenticated path at all. ExemptPath
// is under the reserved /_platform/ prefix and is answered with a 404 before the SDK sees it, so nothing, not
// even a catch-all web handler, can ever be served through the exemption.
const ExemptPath = "/_platform/no-unauthenticated-path"

// Config describes one portal instance.
type Config struct {
	AppID   string // the portal's own app id: the aud the assertion must carry
	Issuer  string // exact iss, https://<auth host>
	JWKSURL string // fixed in-cluster URL of platform-api's JWKS
	// JWKSCAFile is the optional PEM CA bundle trusted for an https JWKSURL; empty means the system roots.
	JWKSCAFile string
	// Pool must run as postgres.PortalRole (postgres.OpenPortalPool).
	Pool *pgxpool.Pool
	// ProbeInterval is PORTAL_PROBE_INTERVAL (zero: the probe is disabled), used to judge probe results.
	ProbeInterval time.Duration
	// Web serves the SPA outside /api/ (a later part); nil answers 404.
	Web            http.Handler
	RequestTimeout time.Duration // zero means httpx.RequestTimeout
	Logger         *slog.Logger
}

// App holds the handlers of both listeners.
type App struct {
	// Public is reached through Traefik with the ForwardAuth middleware: the API (and, later, the SPA).
	Public http.Handler
	// Internal serves the probes to the cluster only.
	Internal http.Handler
	// MetricsHandler serves /metrics on its own listener (PORTAL_METRICS_ADDR), never on Public or Internal.
	MetricsHandler http.Handler
	// Metrics is the registry behind /metrics: one per App, never the global default.
	Metrics *prometheus.Registry
	// ProbeObserver is the observer to hand to the probe job (probe.Config.Observer).
	ProbeObserver probe.Observer
	// Store runs units of work; the mutation use cases of later parts take it.
	Store portal.Store
}

// issuerHost returns the host of the issuer URL (https://<auth host>): the host no deployment may use.
func issuerHost(issuer string) (string, error) {
	u, err := url.Parse(issuer)
	if err != nil || u.Hostname() == "" {
		return "", errors.New("portalapp: the issuer must be an absolute URL with a host")
	}
	return strings.ToLower(u.Hostname()), nil
}

// New builds the app.
func New(cfg Config) (*App, error) {
	if cfg.Pool == nil || cfg.AppID == "" || cfg.Issuer == "" || cfg.JWKSURL == "" {
		return nil, errors.New("portalapp: pool, app id, issuer and JWKS URL are required")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}

	if u, err := url.Parse(cfg.JWKSURL); err == nil && u.Scheme == "http" {
		log.Warn("the JWKS URL is plain http: the integrity of the keys relies on the NetworkPolicy; use https")
	}
	vcfg := platformauth.Config{Issuer: cfg.Issuer, Audience: cfg.AppID, JWKSURL: cfg.JWKSURL, HealthPath: ExemptPath}
	if cfg.JWKSCAFile != "" {
		roots, err := platformauth.LoadRootCAs(cfg.JWKSCAFile)
		if err != nil {
			return nil, fmt.Errorf("JWKS CA file: %w", err)
		}
		vcfg.RootCAs = roots
	}
	verifier, err := platformauth.NewVerifier(vcfg)
	if err != nil {
		return nil, fmt.Errorf("create assertion verifier: %w", err)
	}
	authHost, err := issuerHost(cfg.Issuer)
	if err != nil {
		return nil, err
	}
	reg, err := metrics.NewRegistry()
	if err != nil {
		return nil, err
	}
	pm, err := metrics.NewPortal(reg, cfg.ProbeInterval)
	if err != nil {
		return nil, err
	}
	if err := metrics.RegisterPortalState(reg, metrics.PortalSources{Pool: cfg.Pool, ProbeStatus: postgres.NewProbeRepo(cfg.Pool)}); err != nil {
		return nil, err
	}

	store := postgres.NewPortalStore(cfg.Pool)
	svc, err := portal.NewService(portal.Config{
		Permissions:   postgres.NewPermissionsRepo(cfg.Pool),
		Home:          postgres.NewHomeRepo(cfg.Pool),
		Store:         store,
		PortalAppID:   cfg.AppID,
		AuthHost:      authHost,
		ProbeInterval: cfg.ProbeInterval,
	})
	if err != nil {
		return nil, err
	}
	public, err := portalhttp.New(portalhttp.Config{
		Service: svc,
		Admin:   svc,
		Auth: func(next http.Handler) http.Handler {
			validate := verifier.MiddlewareWithLogger(log, next)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == ExemptPath {
					httpx.WriteProblem(w, httpx.Problem{Status: http.StatusNotFound})
					return
				}
				validate.ServeHTTP(w, r)
			})
		},
		Web:            cfg.Web,
		Observer:       pm,
		RequestTimeout: cfg.RequestTimeout,
		Logger:         log,
	})
	if err != nil {
		return nil, err
	}

	internal := http.NewServeMux()
	internal.Handle("GET "+health.PathLive, health.Live())
	internal.Handle("GET "+health.PathReady, health.ReadyDB(cfg.Pool, log))
	mux := http.NewServeMux()
	mux.Handle(metrics.Route, metrics.Handler(reg))

	return &App{
		Public:         public,
		Internal:       httpx.Recover(log, internal),
		MetricsHandler: httpx.Recover(log, mux),
		Metrics:        reg,
		// The probe job is built by the binary, which owns the probe configuration.
		ProbeObserver: pm,
		Store:         store,
	}, nil
}
