package main

import (
	"fmt"
	"strings"
)

// Environment variable names.
const (
	envListenAddr = "HELLO_LISTEN_ADDR"
	envAppID      = "HELLO_APP_ID"
	envIssuer     = "HELLO_ISSUER"
	envJWKSURL    = "HELLO_JWKS_URL"
)

// config is the app configuration; every field is mandatory.
type config struct {
	ListenAddr string // e.g. ":8080"
	AppID      string // stable app id; the assertion aud must match it
	Issuer     string // platform issuer, exact match on iss
	JWKSURL    string // fixed in-cluster JWKS URL
}

// loadConfig reads the configuration through getenv and reports every missing variable at once.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		ListenAddr: strings.TrimSpace(getenv(envListenAddr)),
		AppID:      strings.TrimSpace(getenv(envAppID)),
		Issuer:     strings.TrimSpace(getenv(envIssuer)),
		JWKSURL:    strings.TrimSpace(getenv(envJWKSURL)),
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
	return cfg, nil
}
