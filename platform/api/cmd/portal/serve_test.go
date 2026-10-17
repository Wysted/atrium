package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/postgres"
)

func TestCheckLoginRole(t *testing.T) {
	t.Parallel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name    string
		ident   postgres.RuntimeIdentity
		allow   bool
		wantErr string
	}{
		{"dedicated login role", postgres.RuntimeIdentity{SessionUser: "portal_login"}, false, ""},
		{"superuser", postgres.RuntimeIdentity{SessionUser: "platform", SessionIsSuper: true}, false, "superuser"},
		{"owner of tables", postgres.RuntimeIdentity{SessionUser: "platform", SessionOwnsData: true}, false, "owns"},
		{"bypasses rls", postgres.RuntimeIdentity{SessionBypassRLS: true}, false, "row-level security"},
		{"can create in public", postgres.RuntimeIdentity{SessionCanCreate: true}, false, "create objects"},
		{"development override", postgres.RuntimeIdentity{SessionIsSuper: true}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkLoginRole(tt.ident, tt.allow, log)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("err = %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), postgres.PortalRole)):
				t.Fatalf("err = %v, want one mentioning %q and %s", err, tt.wantErr, postgres.PortalRole)
			}
		})
	}
}

func TestShutdownBudgetStaysUnderKubernetesGracePeriod(t *testing.T) {
	t.Parallel()
	const defaultTerminationGracePeriod = 30 * time.Second
	if shutdownTimeout >= defaultTerminationGracePeriod {
		t.Fatalf("shutdownTimeout %s must stay under %s", shutdownTimeout, defaultTerminationGracePeriod)
	}
}

func TestWaitBounded(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !waitBounded(ctx, func() {}) {
		t.Fatal("a finished wait reported as expired")
	}
	short, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	block := make(chan struct{})
	defer close(block)
	if waitBounded(short, func() { <-block }) {
		t.Fatal("a blocked wait reported as finished")
	}
}

// The development flag is logged at Error level on every start where it is enabled, even with a clean login role.
func TestPrivilegedLoginFlagIsLoggedAsAnError(t *testing.T) {
	t.Parallel()
	for name, ident := range map[string]postgres.RuntimeIdentity{
		"privileged login role": {SessionUser: "platform", SessionIsSuper: true},
		"clean login role":      {SessionUser: "portal_login"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			if err := checkLoginRole(ident, true, slog.New(slog.NewJSONHandler(&buf, nil))); err != nil {
				t.Fatal(err)
			}
			if out := buf.String(); !strings.Contains(out, `"level":"ERROR"`) || !strings.Contains(out, envAllowPrivilegedDBLogin) {
				t.Fatalf("log = %q, want an ERROR line naming the flag", out)
			}
		})
	}
	var buf bytes.Buffer
	if err := checkLoginRole(postgres.RuntimeIdentity{SessionUser: "portal_login"}, false, slog.New(slog.NewJSONHandler(&buf, nil))); err != nil || buf.Len() != 0 {
		t.Fatalf("a clean start without the flag must be silent: %v %q", err, buf.String())
	}
}

type fakeLookup struct {
	key   string
	found bool
	err   error
}

func (f fakeLookup) PortalAppInitiativeKey(context.Context, string) (string, bool, error) {
	return f.key, f.found, f.err
}

func TestCheckPortalAppWarnsAndNeverFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		repo fakeLookup
		want string
	}{
		{"verified", fakeLookup{key: "platform", found: true}, "portal app verified"},
		{"unknown app", fakeLookup{}, "does not match any app"},
		{"other initiative", fakeLookup{key: "hello", found: true}, "belongs to another initiative"},
		{"database error", fakeLookup{err: errors.New("down")}, "cannot verify the portal app"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			checkPortalApp(t.Context(), tt.repo, "app_x", slog.New(slog.NewTextHandler(&buf, nil)))
			if !strings.Contains(buf.String(), tt.want) {
				t.Fatalf("log = %q, want %q", buf.String(), tt.want)
			}
		})
	}
}
