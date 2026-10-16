package main

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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
	// envAllowPrivilegedDBLogin is a DEVELOPMENT-ONLY escape hatch (default false), like in platform-api: the
	// portal role is assumed with a startup parameter, so a privileged login role could undo it with
	// RESET ROLE. Setting it only downgrades the refusal to a loud WARN. Never set it in production.
	envAllowPrivilegedDBLogin = "PORTAL_ALLOW_PRIVILEGED_DB_LOGIN"
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
	JWKSURL      string // in-cluster URL of platform-api's JWKS (internal listener)
	// AllowPrivilegedDBLogin is the value of PORTAL_ALLOW_PRIVILEGED_DB_LOGIN (development only).
	AllowPrivilegedDBLogin bool
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
	}
	if c.PublicAddr != "" && c.PublicAddr == c.InternalAddr {
		errs = append(errs, fmt.Errorf("%s and %s must differ: the probes must never share a listener with the public routes", envPublicAddr, envInternalAddr))
	}
	for _, other := range []struct{ name, addr string }{{envPublicAddr, c.PublicAddr}, {envInternalAddr, c.InternalAddr}} {
		if c.MetricsAddr != "" && c.MetricsAddr == other.addr {
			errs = append(errs, fmt.Errorf("%s and %s must differ: /metrics must have a listener of its own", envMetricsAddr, other.name))
		}
	}
	if c.AppID != "" && !appIDPattern.MatchString(c.AppID) {
		errs = append(errs, fmt.Errorf("%s must match %s", envAppID, appIDPattern))
	}
	if c.Issuer != "" {
		if u, err := url.Parse(c.Issuer); err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			errs = append(errs, fmt.Errorf("%s must be an https origin such as https://auth.apps.example.com", envIssuer))
		}
	}
	if c.JWKSURL != "" {
		// Usually plain http to the in-cluster service: the integrity of the keys rests on the NetworkPolicy.
		if u, err := url.Parse(c.JWKSURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			errs = append(errs, fmt.Errorf("%s must be an absolute http(s) URL without credentials, query or fragment", envJWKSURL))
		}
	}
	if v := strings.TrimSpace(getenv(envAllowPrivilegedDBLogin)); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", envAllowPrivilegedDBLogin, err))
		}
		c.AllowPrivilegedDBLogin = b
	}
	var perrs []error
	c.Probe, perrs = loadProbeConfig(getenv)
	errs = append(errs, perrs...)
	if err := errors.Join(errs...); err != nil {
		return config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, nil
}
