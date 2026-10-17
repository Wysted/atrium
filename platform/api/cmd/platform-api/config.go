package main

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/httpx"
	"github.com/Wysted/atrium/platform/api/internal/keys"
	"github.com/Wysted/atrium/platform/api/internal/secretfile"
	"github.com/Wysted/atrium/platform/api/internal/session"
)

// Environment variables read by "serve". The database comes from the standard PG* variables.
const (
	envInternalAddr = "PLATFORM_INTERNAL_ADDR"
	envPublicAddr   = "PLATFORM_PUBLIC_ADDR"
	// envMetricsAddr is the listener of /metrics alone: a NetworkPolicy filters by port, so it cannot share one
	// with ForwardAuth or the JWKS.
	envMetricsAddr = "PLATFORM_METRICS_ADDR"
	// envJWKSAddr is the listener of the JWKS alone, the only port app namespaces may reach.
	envJWKSAddr = "PLATFORM_JWKS_ADDR"
	// envJWKSTLSCertFile and envJWKSTLSKeyFile (PEM) switch the JWKS listener to HTTPS. Both or neither; the
	// files may be replaced at any time (see tlsreload).
	envJWKSTLSCertFile = "PLATFORM_JWKS_TLS_CERT_FILE"
	envJWKSTLSKeyFile  = "PLATFORM_JWKS_TLS_KEY_FILE"
	envAuthHost        = "PLATFORM_AUTH_HOST"
	envIssuer          = "PLATFORM_ISSUER"
	envKEKFile         = keys.KEKFileEnv // PLATFORM_KEK_FILE
	envIdleTimeout     = "PLATFORM_IDLE_TIMEOUT"
	envFakeLogin       = "PLATFORM_FAKE_LOGIN"
	// envPortalAppID is the optional id of the portal app, the one app any active user may open (block 6).
	envPortalAppID = "PLATFORM_PORTAL_APP_ID"
	// Google login. All three are set together or none is (see loadConfig).
	envGoogleClientID         = "PLATFORM_GOOGLE_CLIENT_ID"
	envGoogleClientSecretFile = "PLATFORM_GOOGLE_CLIENT_SECRET_FILE"
	envAllowedHD              = "PLATFORM_ALLOWED_HD"
	// envAllowPrivilegedDBLogin is a DEVELOPMENT-ONLY escape hatch (default false). By default "serve" refuses to
	// start when the database login role (session_user) is a superuser, bypasses RLS, owns tables of schema
	// public or can CREATE there: the runtime role is assumed with a startup parameter, so such a login can
	// undo it with RESET ROLE / SET ROLE and a SQL injection would regain full privileges. Setting it to true
	// only downgrades that refusal to a loud WARN. Never set it in production.
	envAllowPrivilegedDBLogin = "PLATFORM_ALLOW_PRIVILEGED_DB_LOGIN"
)

// maxClientSecretBytes bounds the Google client secret file (real secrets are about 35 bytes).
const maxClientSecretBytes = 4 << 10

// appIDPattern matches apps.id (the CHECK of migration 0001).
var appIDPattern = regexp.MustCompile(`^app_[0-9A-HJKMNP-TV-Z]{26}$`)

// hostPattern matches the host form stored in deployments.host (lowercase, no port).
var hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// config is the validated runtime configuration of "serve".
type config struct {
	InternalAddr string
	PublicAddr   string
	MetricsAddr  string
	JWKSAddr     string
	// JWKSTLS is nil when the JWKS listener serves plain HTTP (no TLS variable set).
	JWKSTLS     *jwksTLSConfig
	AuthHost    string
	Issuer      string
	KEKFile     string
	IdleTimeout time.Duration
	// FakeLogin is the parsed PLATFORM_FAKE_LOGIN. It is only honored without Google (see googleConfig).
	FakeLogin bool
	// PortalAppID is PLATFORM_PORTAL_APP_ID; empty means there is no portal app.
	PortalAppID string
	// Google is nil when no Google variable is set (only valid with FakeLogin). When present, the fake login is
	// switched off by the auth host with a WARN: the two never coexist.
	Google *googleConfig
	// AllowPrivilegedDBLogin is the value of PLATFORM_ALLOW_PRIVILEGED_DB_LOGIN (development only).
	AllowPrivilegedDBLogin bool
}

// jwksTLSConfig holds the PEM paths of the JWKS listener certificate and key.
type jwksTLSConfig struct {
	CertFile string
	KeyFile  string
}

// googleConfig is the validated Google login configuration. The secret was read from its file at load time.
type googleConfig struct {
	ClientID     string
	ClientSecret string
	AllowedHD    []string // lowercase, deduplicated, non-empty
}

// loadConfig reads and validates the environment through getenv, reporting every problem at once.
func loadConfig(getenv func(string) string) (config, error) {
	var errs []error
	required := func(name string) string {
		v := strings.TrimSpace(getenv(name))
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
		return v
	}
	c := config{
		InternalAddr: required(envInternalAddr),
		PublicAddr:   required(envPublicAddr),
		MetricsAddr:  required(envMetricsAddr),
		JWKSAddr:     required(envJWKSAddr),
		AuthHost:     required(envAuthHost),
		Issuer:       required(envIssuer),
		KEKFile:      required(envKEKFile),
		IdleTimeout:  session.DefaultIdleTimeout,
	}
	errs = append(errs, httpx.ListenerConflicts([]httpx.NamedAddr{
		{Name: envInternalAddr, Addr: c.InternalAddr},
		{Name: envPublicAddr, Addr: c.PublicAddr},
		{Name: envMetricsAddr, Addr: c.MetricsAddr},
		{Name: envJWKSAddr, Addr: c.JWKSAddr},
	})...)
	certFile := strings.TrimSpace(getenv(envJWKSTLSCertFile))
	keyFile := strings.TrimSpace(getenv(envJWKSTLSKeyFile))
	switch {
	case certFile != "" && keyFile != "":
		c.JWKSTLS = &jwksTLSConfig{CertFile: certFile, KeyFile: keyFile}
	case certFile != "" || keyFile != "":
		errs = append(errs, fmt.Errorf("%s and %s must be set together", envJWKSTLSCertFile, envJWKSTLSKeyFile))
	}
	if c.AuthHost != "" && !hostPattern.MatchString(c.AuthHost) {
		errs = append(errs, fmt.Errorf("%s must be a lowercase host without scheme, port or path", envAuthHost))
	}
	if c.Issuer != "" && c.AuthHost != "" {
		if u, err := url.Parse(c.Issuer); err != nil || u.Scheme != "https" || u.Host != c.AuthHost || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			errs = append(errs, fmt.Errorf("%s must be exactly https://<%s>", envIssuer, envAuthHost))
		}
	}
	if v := strings.TrimSpace(getenv(envIdleTimeout)); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", envIdleTimeout, err))
		case d < session.MinIdleTimeout || d > session.MaxIdleTimeout:
			errs = append(errs, fmt.Errorf("%s must be within [%s, %s]", envIdleTimeout, session.MinIdleTimeout, session.MaxIdleTimeout))
		default:
			c.IdleTimeout = d
		}
	}
	if v := strings.TrimSpace(getenv(envFakeLogin)); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", envFakeLogin, err))
		}
		c.FakeLogin = b
	}
	if v := strings.TrimSpace(getenv(envAllowPrivilegedDBLogin)); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", envAllowPrivilegedDBLogin, err))
		}
		c.AllowPrivilegedDBLogin = b
	}
	if v := strings.TrimSpace(getenv(envPortalAppID)); v != "" {
		if !appIDPattern.MatchString(v) {
			errs = append(errs, fmt.Errorf("%s must match %s", envPortalAppID, appIDPattern))
		}
		c.PortalAppID = v
	}
	g, gerrs := loadGoogleConfig(getenv)
	errs = append(errs, gerrs...)
	c.Google = g
	if g == nil && len(gerrs) == 0 && !c.FakeLogin {
		errs = append(errs, fmt.Errorf("%s=false requires Google login: set %s, %s and %s",
			envFakeLogin, envGoogleClientID, envGoogleClientSecretFile, envAllowedHD))
	}
	if err := errors.Join(errs...); err != nil {
		return config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, nil
}

// loadGoogleConfig returns nil, nil when none of the Google variables is set. If any is set, all are required
// and validated: a half-configured login must fail at start-up, not at the first user.
func loadGoogleConfig(getenv func(string) string) (*googleConfig, []error) {
	id := strings.TrimSpace(getenv(envGoogleClientID))
	secretPath := strings.TrimSpace(getenv(envGoogleClientSecretFile))
	hdRaw := strings.TrimSpace(getenv(envAllowedHD))
	if id == "" && secretPath == "" && hdRaw == "" {
		return nil, nil
	}
	var errs []error
	g := &googleConfig{ClientID: id}
	if id == "" {
		errs = append(errs, fmt.Errorf("%s is required when Google login is configured", envGoogleClientID))
	} else if strings.ContainsAny(id, " \t\r\n") {
		errs = append(errs, fmt.Errorf("%s must not contain whitespace", envGoogleClientID))
	}
	if secretPath == "" {
		errs = append(errs, fmt.Errorf("%s is required when Google login is configured", envGoogleClientSecretFile))
	} else if raw, err := secretfile.Read(secretPath, maxClientSecretBytes); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", envGoogleClientSecretFile, err))
	} else {
		g.ClientSecret = strings.TrimSpace(string(raw)) // a mounted file usually ends with a newline
		clear(raw)
		if g.ClientSecret == "" {
			errs = append(errs, fmt.Errorf("%s: the file is empty", envGoogleClientSecretFile))
		}
	}
	if hdRaw == "" {
		errs = append(errs, fmt.Errorf("%s is required when Google login is configured", envAllowedHD))
	} else {
		hds, err := parseAllowedHD(hdRaw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", envAllowedHD, err))
		}
		g.AllowedHD = hds
	}
	return g, errs
}

// parseAllowedHD splits the comma-separated hosted-domain list, lowercases it and rejects empty or malformed
// entries (a typo such as "a.com,,b.com" is reported instead of silently ignored). Duplicates collapse.
func parseAllowedHD(raw string) ([]string, error) {
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		hd := strings.ToLower(strings.TrimSpace(part))
		if hd == "" || !strings.Contains(hd, ".") || !hostPattern.MatchString(hd) {
			return nil, fmt.Errorf("invalid domain %q: want a comma-separated list of domains such as example.com", part)
		}
		if !slices.Contains(out, hd) {
			out = append(out, hd)
		}
	}
	return out, nil
}
