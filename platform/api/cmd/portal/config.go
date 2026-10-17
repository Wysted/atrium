package main

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"platform/api/internal/httpx"
	"platform/api/internal/portal"
	"platform/api/internal/webui"
)

// Environment variables read by "serve". The database comes from the standard PG* variables, connecting
// with a login role that is only a member of platform_portal.
const (
	envPublicAddr   = "PORTAL_PUBLIC_ADDR"
	envInternalAddr = "PORTAL_INTERNAL_ADDR"
	// envMetricsAddr is the listener of /metrics alone, like in platform-api: a NetworkPolicy filters by port.
	envMetricsAddr = "PORTAL_METRICS_ADDR"
	envAppID       = "PORTAL_APP_ID"
	envIssuer      = "PORTAL_ISSUER"
	envJWKSURL     = "PORTAL_JWKS_URL"
	// envJWKSCAFile is the optional PEM CA bundle for an https PORTAL_JWKS_URL signed by a private CA.
	envJWKSCAFile = "PORTAL_JWKS_CA_FILE"
	// envAllowPrivilegedDBLogin is a DEVELOPMENT-ONLY escape hatch (default false), like in platform-api: the
	// portal role is assumed with a startup parameter, so a privileged login role could undo it with
	// RESET ROLE. Setting it only downgrades the refusal to a loud WARN. Never set it in production.
	envAllowPrivilegedDBLogin = "PORTAL_ALLOW_PRIVILEGED_DB_LOGIN"
	// Caps of the lifetime of an API token, one per owner kind (contract block 6, "Non-browser clients"). Optional Go
	// durations above zero; a request without expires_in gets the cap. Raising one is the adopter's decision.
	envTokenMaxTTLPersonal = "PORTAL_TOKEN_MAX_TTL_PERSONAL" // default 720h
	envTokenMaxTTLRobot    = "PORTAL_TOKEN_MAX_TTL_ROBOT"    // default 2160h; used by robot tokens (a later part)
)

// appIDPattern matches apps.id (the CHECK of migration 0001).
var appIDPattern = regexp.MustCompile(`^app_[0-9A-HJKMNP-TV-Z]{26}$`)

// config is the validated runtime configuration of "serve"; every field but the flag is mandatory.
type config struct {
	PublicAddr   string
	InternalAddr string
	MetricsAddr  string
	AppID        string // the portal's own app id: the aud of its assertions
	Issuer       string // https://<auth host>, exact match on iss
	JWKSURL      string // in-cluster URL of platform-api's JWKS (JWKS listener of platform-api)
	JWKSCAFile   string // optional CA bundle for an https JWKSURL; empty means system roots
	// AllowPrivilegedDBLogin is the value of PORTAL_ALLOW_PRIVILEGED_DB_LOGIN (development only).
	AllowPrivilegedDBLogin bool
	// TokenMaxTTLPersonal and TokenMaxTTLRobot are PORTAL_TOKEN_MAX_TTL_PERSONAL and PORTAL_TOKEN_MAX_TTL_ROBOT.
	TokenMaxTTLPersonal time.Duration
	TokenMaxTTLRobot    time.Duration
	// Probe configures the integration probe (PORTAL_PROBE_*); Probe.Interval 0 disables it.
	Probe probeConfig
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
		PublicAddr:   required(envPublicAddr),
		InternalAddr: required(envInternalAddr),
		MetricsAddr:  required(envMetricsAddr),
		AppID:        required(envAppID),
		Issuer:       required(envIssuer),
		JWKSURL:      required(envJWKSURL),
		JWKSCAFile:   strings.TrimSpace(getenv(envJWKSCAFile)),
	}
	errs = append(errs, httpx.ListenerConflicts([]httpx.NamedAddr{
		{Name: envPublicAddr, Addr: c.PublicAddr},
		{Name: envInternalAddr, Addr: c.InternalAddr},
		{Name: envMetricsAddr, Addr: c.MetricsAddr},
	})...)
	if c.AppID != "" && !appIDPattern.MatchString(c.AppID) {
		errs = append(errs, fmt.Errorf("%s must match %s", envAppID, appIDPattern))
	}
	if c.Issuer != "" {
		if _, err := webui.ParseAuthOrigin(c.Issuer); err != nil {
			errs = append(errs, fmt.Errorf("%s must be an https origin (host[:port] with a port in 1-65535) such as https://auth.apps.example.com: %w", envIssuer, err))
		}
	}
	if c.JWKSURL != "" {
		// https is recommended; plain http is still accepted (serve warns): the keys then rest on the NetworkPolicy.
		if u, err := url.Parse(c.JWKSURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			errs = append(errs, fmt.Errorf("%s must be an absolute http(s) URL without credentials, query or fragment", envJWKSURL))
		}
	}
	if c.JWKSCAFile != "" && !isScheme(c.JWKSURL, "https") {
		errs = append(errs, fmt.Errorf("%s only applies to an https %s", envJWKSCAFile, envJWKSURL))
	}
	if v := strings.TrimSpace(getenv(envAllowPrivilegedDBLogin)); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", envAllowPrivilegedDBLogin, err))
		}
		c.AllowPrivilegedDBLogin = b
	}
	var terrs []error
	c.TokenMaxTTLPersonal, terrs = tokenTTL(getenv, envTokenMaxTTLPersonal, portal.DefaultTokenMaxTTLPersonal)
	errs = append(errs, terrs...)
	c.TokenMaxTTLRobot, terrs = tokenTTL(getenv, envTokenMaxTTLRobot, portal.DefaultTokenMaxTTLRobot)
	errs = append(errs, terrs...)
	var perrs []error
	c.Probe, perrs = loadProbeConfig(getenv)
	errs = append(errs, perrs...)
	if err := errors.Join(errs...); err != nil {
		return config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, nil
}

// isScheme reports whether rawURL parses and has the given scheme (url.Parse lowercases it, so HTTPS:// counts).
func isScheme(rawURL, scheme string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Scheme == scheme
}

// tokenTTL reads an optional token lifetime cap: a Go duration above zero, def when unset.
func tokenTTL(getenv func(string) string, name string, def time.Duration) (time.Duration, []error) {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def, []error{fmt.Errorf("%s: %w", name, err)}
	}
	if d <= 0 {
		return def, []error{fmt.Errorf("%s must be greater than zero", name)}
	}
	return d, nil
}
