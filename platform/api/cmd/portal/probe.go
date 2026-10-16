package main

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/postgres"
	"platform/api/internal/probe"
)

// Environment variables of the integration probe (contract block 6, "Observability").
const (
	// envProbeInterval is how often every deployment host is probed; 0 disables the probe. Default 5m.
	envProbeInterval = "PORTAL_PROBE_INTERVAL"
	// envProbeAddr is the host:port the probe connects to, whatever the deployment host is (the Traefik service
	// inside the cluster). TLS server name and Host stay the deployment host. Required when the probe is enabled.
	envProbeAddr = "PORTAL_PROBE_ADDR"
	// envProbeCAFile is an optional PEM bundle added to the system roots to verify the entrypoint certificate.
	envProbeCAFile = "PORTAL_PROBE_CA_FILE"
	// envProbeInsecureTLS is a DEVELOPMENT-ONLY escape hatch (default false) that skips the certificate
	// verification of the probe; it is logged at Error level on every start.
	envProbeInsecureTLS = "PORTAL_PROBE_INSECURE_TLS"
)

// maxProbeCABytes bounds the CA bundle file.
const maxProbeCABytes = 1 << 20

// probeConfig is the validated configuration of the probe. Interval 0 means disabled (the rest is ignored).
type probeConfig struct {
	Interval    time.Duration
	Addr        string
	RootCAs     *x509.CertPool // nil: the system roots
	InsecureTLS bool
}

// loadProbeConfig reads and validates the probe variables, reporting every problem.
func loadProbeConfig(getenv func(string) string) (probeConfig, []error) {
	var errs []error
	c := probeConfig{Interval: probe.DefaultInterval}
	if v := strings.TrimSpace(getenv(envProbeInterval)); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", envProbeInterval, err))
		case d == 0:
			return probeConfig{}, nil // disabled: nothing else is read
		case d < probe.MinInterval || d > probe.MaxInterval:
			errs = append(errs, fmt.Errorf("%s must be 0 (disabled) or within [%s, %s]", envProbeInterval, probe.MinInterval, probe.MaxInterval))
		default:
			c.Interval = d
		}
	}
	// Required: without it the probe would resolve the hostnames written by admins itself (blind SSRF).
	if v := strings.TrimSpace(getenv(envProbeAddr)); v == "" {
		errs = append(errs, fmt.Errorf("%s is required when the probe is enabled (set it to the entrypoint, for example traefik.traefik.svc.cluster.local:443, or set %s=0 to disable the probe)", envProbeAddr, envProbeInterval))
	} else {
		if host, port, err := net.SplitHostPort(v); err != nil || host == "" || !validPort(port) {
			errs = append(errs, fmt.Errorf("%s must be host:port, for example traefik.traefik.svc.cluster.local:443", envProbeAddr))
		}
		c.Addr = v
	}
	if v := strings.TrimSpace(getenv(envProbeInsecureTLS)); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", envProbeInsecureTLS, err))
		}
		c.InsecureTLS = b
	}
	if path := strings.TrimSpace(getenv(envProbeCAFile)); path != "" {
		pool, err := loadCABundle(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", envProbeCAFile, err))
		}
		c.RootCAs = pool
	}
	return c, errs
}

func validPort(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}

// loadCABundle returns the system roots plus the certificates of the PEM file at path. A certificate is public
// data, so unlike a secret the file may be world-readable (a ConfigMap mount usually is).
func loadCABundle(path string) (*x509.CertPool, error) {
	f, err := os.Open(path) //nolint:gosec // G304: operator-configured path, never request input
	if err != nil {
		return nil, fmt.Errorf("open CA bundle: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only handle
	raw, err := io.ReadAll(io.LimitReader(f, maxProbeCABytes+1))
	if err != nil {
		return nil, fmt.Errorf("read CA bundle: %w", err)
	}
	if len(raw) > maxProbeCABytes {
		return nil, errors.New("CA bundle is too large")
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(raw) {
		return nil, errors.New("no PEM certificate found in the CA bundle")
	}
	return pool, nil
}

// newProbeJob builds the probe job, or returns nil when the probe is disabled. authHost is the host of the
// issuer: a deployment is protected when it redirects to https://<authHost>/login.
func newProbeJob(cfg probeConfig, issuer, portalAppID string, pool *pgxpool.Pool, obs probe.Observer, log *slog.Logger) (*probe.Job, error) {
	if cfg.Interval <= 0 {
		log.Info("integration probe disabled", "env", envProbeInterval)
		return nil, nil
	}
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("probe: cannot take the auth host from the issuer %q", issuer)
	}
	if cfg.InsecureTLS {
		// Error level on every start: the flag must never go unnoticed.
		log.Error(envProbeInsecureTLS + " is enabled: the probe does not verify the certificate of the entrypoint, so it cannot tell a protected host from an impostor. Development only, never use this in production")
	}
	checker, err := probe.NewChecker(probe.CheckerConfig{
		AuthHost: u.Host, Addr: cfg.Addr, RootCAs: cfg.RootCAs, InsecureSkipVerify: cfg.InsecureTLS,
	})
	if err != nil {
		return nil, err
	}
	repo := postgres.NewProbeRepo(pool)
	job, err := probe.NewJob(probe.Config{
		Store: repo, Checker: checker, Interval: cfg.Interval, ControlAppID: portalAppID, Observer: obs, Logger: log,
	})
	if err != nil {
		return nil, err
	}
	log.Info("integration probe enabled", "interval", cfg.Interval.String(), "dial_addr", cfg.Addr, "auth_host", u.Host, "control_app_id", portalAppID)
	return job, nil
}
