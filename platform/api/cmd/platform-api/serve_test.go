package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"platform/api/internal/postgres"
)

func TestWaitBounded(t *testing.T) {
	t.Run("finishes in time", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if !waitBounded(ctx, func() { time.Sleep(10 * time.Millisecond) }) {
			t.Fatal("reported expiry for a fast wait")
		}
	})
	t.Run("gives up when the budget ends, even if wait hangs", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		block := make(chan struct{})
		defer close(block)
		start := time.Now()
		if waitBounded(ctx, func() { <-block }) {
			t.Fatal("reported success for a hung wait")
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("returned after %s", d)
		}
	})
}

// The whole shutdown shares one budget that fits the Kubernetes default grace period.
func TestShutdownBudgetFitsTheGracePeriod(t *testing.T) {
	const defaultTerminationGracePeriod = 30 * time.Second
	if shutdownTimeout >= defaultTerminationGracePeriod {
		t.Fatalf("shutdownTimeout %s must stay under %s", shutdownTimeout, defaultTerminationGracePeriod)
	}
}

func TestCheckLoginRole(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name      string
		ident     postgres.RuntimeIdentity
		allow     bool
		wantErr   bool
		wantInErr string
	}{
		{name: "dedicated login role", ident: postgres.RuntimeIdentity{SessionUser: "platform_login"}},
		{name: "superuser", ident: postgres.RuntimeIdentity{SessionUser: "postgres", SessionIsSuper: true}, wantErr: true, wantInErr: "superuser"},
		{name: "bypassrls", ident: postgres.RuntimeIdentity{SessionUser: "x", SessionBypassRLS: true}, wantErr: true, wantInErr: "row-level security"},
		{name: "table owner", ident: postgres.RuntimeIdentity{SessionUser: "x", SessionOwnsData: true}, wantErr: true, wantInErr: "owns"},
		{name: "can create in public", ident: postgres.RuntimeIdentity{SessionUser: "x", SessionCanCreate: true}, wantErr: true, wantInErr: "create objects"},
		{name: "superuser allowed by dev flag", ident: postgres.RuntimeIdentity{SessionIsSuper: true, SessionOwnsData: true}, allow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkLoginRole(tt.ident, tt.allow, log)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				return
			}
			for _, want := range []string{tt.wantInErr, postgres.RuntimeRole, envAllowPrivilegedDBLogin} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

func TestLoadConfigAllowPrivilegedDBLogin(t *testing.T) {
	base := map[string]string{
		envInternalAddr: ":8081", envPublicAddr: ":8080", envAuthHost: "auth.example.test",
		envIssuer: "https://auth.example.test", envKEKFile: "/tmp/kek",
	}
	load := func(extra string) (config, error) {
		return loadConfig(func(k string) string {
			if k == envAllowPrivilegedDBLogin {
				return extra
			}
			return base[k]
		})
	}
	if c, err := load(""); err != nil || c.AllowPrivilegedDBLogin {
		t.Fatalf("default: cfg=%v err=%v, want disabled", c.AllowPrivilegedDBLogin, err)
	}
	if c, err := load("true"); err != nil || !c.AllowPrivilegedDBLogin {
		t.Fatalf("true: cfg=%v err=%v, want enabled", c.AllowPrivilegedDBLogin, err)
	}
	if _, err := load("maybe"); err == nil {
		t.Fatal("invalid value accepted")
	}
}
