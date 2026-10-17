package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/probe"
)

// writeCA writes a self-signed certificate as a PEM file and returns its path.
func writeCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "probe test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil { //nolint:gosec // a CA certificate is public
		t.Fatal(err)
	}
	return path
}

func TestProbeConfig(t *testing.T) {
	t.Parallel()
	ca := writeCA(t)
	notPEM := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		overrides map[string]string
		wantErr   []string
		check     func(t *testing.T, c probeConfig)
	}{
		{"defaults", nil, nil, func(t *testing.T, c probeConfig) {
			if c.Interval != 5*time.Minute || c.Addr != "traefik.traefik.svc.cluster.local:443" || c.InsecureTLS || c.RootCAs != nil {
				t.Fatalf("defaults = %+v", c)
			}
		}},
		{"everything set", map[string]string{
			envProbeInterval: "10m", envProbeAddr: "traefik.traefik.svc.cluster.local:443", envProbeCAFile: ca, envProbeInsecureTLS: "true",
		}, nil, func(t *testing.T, c probeConfig) {
			if c.Interval != 10*time.Minute || c.Addr != "traefik.traefik.svc.cluster.local:443" || !c.InsecureTLS || c.RootCAs == nil {
				t.Fatalf("config = %+v", c)
			}
		}},
		{"zero disables and ignores the rest", map[string]string{envProbeInterval: "0", envProbeAddr: "garbage", envProbeCAFile: "/nonexistent"}, nil,
			func(t *testing.T, c probeConfig) {
				if c.Interval != 0 {
					t.Fatalf("interval = %s, want disabled", c.Interval)
				}
			}},
		{"0s disables", map[string]string{envProbeInterval: "0s"}, nil, func(t *testing.T, c probeConfig) {
			if c.Interval != 0 {
				t.Fatalf("interval = %s", c.Interval)
			}
		}},
		{"not a duration", map[string]string{envProbeInterval: "often"}, []string{envProbeInterval}, nil},
		{"too frequent", map[string]string{envProbeInterval: "5s"}, []string{envProbeInterval}, nil},
		{"negative", map[string]string{envProbeInterval: "-1m"}, []string{envProbeInterval}, nil},
		{"too rare", map[string]string{envProbeInterval: "72h"}, []string{envProbeInterval}, nil},
		{"address required when enabled", map[string]string{envProbeAddr: ""}, []string{envProbeAddr, "required"}, nil},
		{"address required with an explicit interval", map[string]string{envProbeAddr: " ", envProbeInterval: "10m"}, []string{envProbeAddr, "required"}, nil},
		{"address not needed when disabled", map[string]string{envProbeAddr: "", envProbeInterval: "0"}, nil, func(t *testing.T, c probeConfig) {
			if c.Interval != 0 {
				t.Fatalf("interval = %s", c.Interval)
			}
		}},
		{"address without port", map[string]string{envProbeAddr: "traefik.example.test"}, []string{envProbeAddr}, nil},
		{"address without host", map[string]string{envProbeAddr: ":443"}, []string{envProbeAddr}, nil},
		{"address with a bad port", map[string]string{envProbeAddr: "traefik.example.test:https"}, []string{envProbeAddr}, nil},
		{"address with a port out of range", map[string]string{envProbeAddr: "traefik.example.test:70000"}, []string{envProbeAddr}, nil},
		{"bad insecure flag", map[string]string{envProbeInsecureTLS: "maybe"}, []string{envProbeInsecureTLS}, nil},
		{"missing CA file", map[string]string{envProbeCAFile: "/nonexistent/ca.pem"}, []string{envProbeCAFile}, nil},
		{"CA file without certificates", map[string]string{envProbeCAFile: notPEM}, []string{envProbeCAFile}, nil},
		{"every problem at once", map[string]string{envProbeInterval: "x", envProbeAddr: "y", envProbeInsecureTLS: "z"},
			[]string{envProbeInterval, envProbeAddr, envProbeInsecureTLS}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := loadConfig(env(tt.overrides))
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if tt.check != nil {
					tt.check(t, cfg.Probe)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted %+v", cfg.Probe)
			}
			for _, s := range tt.wantErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not mention %q", err, s)
				}
			}
		})
	}
}

func TestNewProbeJob(t *testing.T) {
	t.Parallel()
	newLog := func() (*slog.Logger, *bytes.Buffer) {
		buf := &bytes.Buffer{}
		return slog.New(slog.NewJSONHandler(buf, nil)), buf
	}

	t.Run("interval 0 builds nothing", func(t *testing.T) {
		t.Parallel()
		log, _ := newLog()
		job, err := newProbeJob(probeConfig{}, okIssuer, okAppID, nil, nil, log)
		if err != nil || job != nil {
			t.Fatalf("job %v, err %v: a disabled probe must not exist", job, err)
		}
	})

	t.Run("enabled", func(t *testing.T) {
		t.Parallel()
		log, logs := newLog()
		job, err := newProbeJob(probeConfig{Interval: probe.DefaultInterval, Addr: "traefik:443"}, okIssuer, okAppID, nil, nil, log)
		if err != nil || job == nil {
			t.Fatalf("job %v, err %v", job, err)
		}
		if strings.Contains(logs.String(), `"level":"ERROR"`) {
			t.Fatalf("a normal start logged an error: %s", logs)
		}
	})

	t.Run("the insecure flag is logged at Error level", func(t *testing.T) {
		t.Parallel()
		log, logs := newLog()
		if _, err := newProbeJob(probeConfig{Interval: probe.DefaultInterval, InsecureTLS: true}, okIssuer, okAppID, nil, nil, log); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), envProbeInsecureTLS) {
			t.Fatalf("log = %s", logs)
		}
	})

	t.Run("a bad issuer is refused", func(t *testing.T) {
		t.Parallel()
		log, _ := newLog()
		if _, err := newProbeJob(probeConfig{Interval: probe.DefaultInterval, Addr: "traefik:443"}, "not a url", okAppID, nil, nil, log); err == nil {
			t.Fatal("accepted an issuer without host")
		}
	})
}
