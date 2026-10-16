package main

import (
	"fmt"
	"net/url"
	"strings"
)

// Environment variable names.
const (
	envListenAddr = "HELLO_LISTEN_ADDR"
	envAppID      = "HELLO_APP_ID"
	envIssuer     = "HELLO_ISSUER"
	envJWKSURL    = "HELLO_JWKS_URL"
	envJWKSCAFile = "HELLO_JWKS_CA_FILE" // optional
)

// config is the app configuration; every field is mandatory except JWKSCAFile.
type config struct {
	ListenAddr string // e.g. ":8080"
	AppID      string // stable app id; the assertion aud must match it
	Issuer     string // platform issuer, exact match on iss
	JWKSURL    string // fixed in-cluster JWKS URL (https recommended)
	JWKSCAFile string // optional PEM CA bundle for an https JWKS URL signed by a private CA; empty means system roots
}

// loadConfig reads the configuration through getenv and reports every missing variable at once.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		ListenAddr: strings.TrimSpace(getenv(envListenAddr)),
		AppID:      strings.TrimSpace(getenv(envAppID)),
		Issuer:     strings.TrimSpace(getenv(envIssuer)),
		JWKSURL:    strings.TrimSpace(getenv(envJWKSURL)),
		JWKSCAFile: strings.TrimSpace(getenv(envJWKSCAFile)),
	}
	var missing []string
	for _, v := range []struct{ name, value string }{
		{envListenAddr, cfg.ListenAddr},
		{envAppID, cfg.AppID},
		{envIssuer, cfg.Issuer},
		{envJWKSURL, cfg.JWKSURL},
	} {
		if v.value == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing required environment: %s", strings.Join(missing, ", "))
	}
	// The CA bundle only makes sense for https; the scheme comes from the parsed URL (url.Parse lowercases it).
	if u, err := url.Parse(cfg.JWKSURL); cfg.JWKSCAFile != "" && (err != nil || u.Scheme != "https") {
		return config{}, fmt.Errorf("%s only applies to an https %s", envJWKSCAFile, envJWKSURL)
	}
	return cfg, nil
}
