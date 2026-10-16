package main

import (
	"strings"
	"testing"
)

const (
	okAppID  = "app_0000000000000000000000000P"
	okIssuer = "https://auth.apps.example.test"
	okJWKS   = "http://platform-api-jwks.platform.svc.cluster.local:8083/.well-known/jwks.json"
)

func env(overrides map[string]string) func(string) string {
	base := map[string]string{
		envPublicAddr: ":8080", envInternalAddr: ":8081", envMetricsAddr: ":9090", envAppID: okAppID, envIssuer: okIssuer, envJWKSURL: okJWKS,
		envProbeAddr: "traefik.traefik.svc.cluster.local:443",
	}
	for k, v := range overrides {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		overrides map[string]string
		wantErr   []string // substrings, all must appear
	}{
		{"valid", nil, nil},
		{"valid with https jwks", map[string]string{envJWKSURL: "https://platform-api.example.test/.well-known/jwks.json"}, nil},
		{"missing everything", map[string]string{envPublicAddr: "", envInternalAddr: "", envAppID: "", envIssuer: "", envJWKSURL: ""},
			[]string{envPublicAddr, envInternalAddr, envAppID, envIssuer, envJWKSURL}},
		{"same addr", map[string]string{envInternalAddr: ":8080"}, []string{"different ports"}},
		{"missing metrics addr", map[string]string{envMetricsAddr: ""}, []string{envMetricsAddr}},
		{"metrics on the public addr", map[string]string{envMetricsAddr: ":8080"}, []string{"different ports"}},
		{"metrics on the internal addr", map[string]string{envMetricsAddr: ":8081"}, []string{"different ports"}},
		{"app id not a ulid", map[string]string{envAppID: "portal"}, []string{envAppID}},
		{"app id lowercase ulid", map[string]string{envAppID: "app_0000000000000000000000000p"}, []string{envAppID}},
		{"issuer over http", map[string]string{envIssuer: "http://auth.apps.example.test"}, []string{envIssuer}},
		{"issuer with path", map[string]string{envIssuer: "https://auth.apps.example.test/x"}, []string{envIssuer}},
		{"issuer with empty port", map[string]string{envIssuer: "https://auth.apps.example.test:"}, []string{envIssuer}},
		{"issuer with port 0", map[string]string{envIssuer: "https://auth.apps.example.test:0"}, []string{envIssuer}},
		{"issuer with port 65536", map[string]string{envIssuer: "https://auth.apps.example.test:65536"}, []string{envIssuer}},
		{"issuer with port 99999", map[string]string{envIssuer: "https://auth.apps.example.test:99999"}, []string{envIssuer}},
		{"issuer with signed port", map[string]string{envIssuer: "https://auth.apps.example.test:+1"}, []string{envIssuer}},
		{"issuer with port 443", map[string]string{envIssuer: "https://auth.apps.example.test:443"}, nil},
		{"issuer with query", map[string]string{envIssuer: "https://auth.apps.example.test?a=b"}, []string{envIssuer}},
		{"issuer without host", map[string]string{envIssuer: "https://"}, []string{envIssuer}},
		{"issuer with credentials", map[string]string{envIssuer: "https://u:p@auth.apps.example.test"}, []string{envIssuer}}, //nolint:gosec // invalid input on purpose
		{"jwks relative", map[string]string{envJWKSURL: "/.well-known/jwks.json"}, []string{envJWKSURL}},
		{"jwks wrong scheme", map[string]string{envJWKSURL: "ftp://x/jwks"}, []string{envJWKSURL}},
		{"jwks with credentials", map[string]string{envJWKSURL: "http://u:p@x/jwks"}, []string{envJWKSURL}}, //nolint:gosec // invalid input on purpose
		{"bad privileged flag", map[string]string{envAllowPrivilegedDBLogin: "maybe"}, []string{envAllowPrivilegedDBLogin}},
		{"values are trimmed", map[string]string{envPublicAddr: " :8080 "}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := loadConfig(env(tt.overrides))
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if cfg.AppID != okAppID || cfg.PublicAddr != ":8080" {
					t.Fatalf("cfg = %+v", cfg)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted %+v", cfg)
			}
			for _, s := range tt.wantErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not mention %q", err, s)
				}
			}
		})
	}
}

func TestPrivilegedFlag(t *testing.T) {
	t.Parallel()
	cfg, err := loadConfig(env(map[string]string{envAllowPrivilegedDBLogin: "true"}))
	if err != nil || !cfg.AllowPrivilegedDBLogin {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
	if cfg, err = loadConfig(env(nil)); err != nil || cfg.AllowPrivilegedDBLogin {
		t.Fatalf("default must be off: %+v, %v", cfg, err)
	}
}
