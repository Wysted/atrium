package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wysted/atrium/platform/api/internal/session"
)

type fakeHosts struct {
	registered map[string]bool
	err        error
}

func (f fakeHosts) HostRegistered(_ context.Context, host string) (bool, error) {
	return f.registered[host], f.err
}

// stubs satisfy the other stores; the unit tests below never reach them.
type stubSessions struct{ session.SessionStore }
type stubApps struct{ session.AppSessionStore }
type stubCodes struct{ session.LoginCodeStore }

func newUnitService(t *testing.T, hosts session.HostRegistry) *session.Service {
	t.Helper()
	svc, err := session.NewService(session.Config{Sessions: stubSessions{}, AppSessions: stubApps{}, Codes: stubCodes{}, Hosts: hosts})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestTokens(t *testing.T) {
	v1, h1, err := session.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	v2, _, _ := session.NewToken()
	if v1 == v2 {
		t.Fatal("tokens repeat")
	}
	if len(v1) != 43 || len(h1) != 32 { // 256 bits in base64url; SHA-256
		t.Fatalf("lengths: value %d hash %d", len(v1), len(h1))
	}
	got, ok := session.HashToken(v1)
	if !ok || string(got) != string(h1) {
		t.Fatal("HashToken does not reproduce the stored hash")
	}
	for _, bad := range []string{"", "short", v1 + "A", v1[:42], v1[:42] + "=", v1[:42] + "!", "é" + v1[1:]} {
		if _, ok := session.HashToken(bad); ok {
			t.Fatalf("HashToken accepted %q", bad)
		}
	}
	// Non-canonical encoding (non-zero trailing bits) must not produce a second valid value for the same token.
	last := v1[len(v1)-1]
	for _, alt := range []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") {
		if alt == last {
			continue
		}
		if h, ok := session.HashToken(v1[:42] + string(alt)); ok && string(h) == string(h1) {
			t.Fatalf("two encodings of the same token: %q and %q", v1, v1[:42]+string(alt))
		}
	}
}

func TestNewServiceValidation(t *testing.T) {
	if _, err := session.NewService(session.Config{}); err == nil {
		t.Fatal("empty config accepted")
	}
	cfg := session.Config{Sessions: stubSessions{}, AppSessions: stubApps{}, Codes: stubCodes{}, Hosts: fakeHosts{}}
	cfg.IdleTimeout = session.AbsoluteLifetime + 1
	if _, err := session.NewService(cfg); err == nil {
		t.Fatal("idle longer than the absolute lifetime accepted")
	}
	cfg.IdleTimeout = -1
	if _, err := session.NewService(cfg); err == nil {
		t.Fatal("negative idle accepted")
	}
	for _, d := range []time.Duration{time.Minute, session.MinIdleTimeout - 1, session.MaxIdleTimeout + 1} {
		cfg.IdleTimeout = d
		if _, err := session.NewService(cfg); err == nil {
			t.Fatalf("idle %s outside the contract range accepted", d)
		}
	}
	for _, d := range []time.Duration{0, session.MinIdleTimeout, session.MaxIdleTimeout} { // 0 = default
		cfg.IdleTimeout = d
		if _, err := session.NewService(cfg); err != nil {
			t.Fatalf("idle %s rejected: %v", d, err)
		}
	}
}

func TestValidateRD(t *testing.T) {
	svc := newUnitService(t, fakeHosts{registered: map[string]bool{"app.example.com": true}})
	tests := []struct {
		name, rd, want string // want empty: must be rejected
	}{
		{"plain", "https://app.example.com/a/b?x=1#frag", "https://app.example.com/a/b?x=1#frag"},
		{"host is lowercased, root path added", "https://APP.Example.com", "https://app.example.com/"},
		{"query may carry a URL", "https://app.example.com/p?next=https://x//y", "https://app.example.com/p?next=https://x//y"},
		{"http", "http://app.example.com/", ""},
		{"host not registered", "https://evil.example.org/", ""},
		{"subdomain not registered", "https://x.app.example.com/", ""},
		{"userinfo", "https://user@app.example.com/", ""},
		{"userinfo disguising the host", "https://app.example.com@evil.com/", ""},
		{"double slash path", "https://app.example.com//evil.com", ""},
		{"double slash inside path", "https://app.example.com/a//b", ""},
		{"encoded double slash", "https://app.example.com/%2F%2Fevil.com", ""},
		{"protocol relative", "//evil.com/x", ""},
		{"backslash", `https://app.example.com/\evil.com`, ""},
		{"backslash in authority", `https://evil.com\@app.example.com/`, ""},
		{"encoded backslash", "https://app.example.com/%5Cevil.com", ""},
		{"backslash scheme", `https:\\evil.com`, ""},
		{"javascript", "javascript:alert(1)", ""},
		{"data", "data:text/html,x", ""},
		{"relative", "/relative", ""},
		{"opaque https", "https:app.example.com", ""},
		{"port", "https://app.example.com:8443/", ""},
		{"empty port", "https://app.example.com:/", ""},
		{"trailing dot", "https://app.example.com./", ""},
		{"newline", "https://app.example.com/a\nSet-Cookie: x", ""},
		{"space", "https://app.example.com/a b", ""},
		{"encoded newline", "https://app.example.com/a%0ASet-Cookie:x", ""},
		{"empty", "", ""},
		{"too long", "https://app.example.com/" + string(make([]byte, session.MaxRDLength)), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.ValidateRD(context.Background(), tt.rd)
			if tt.want == "" {
				if !errors.Is(err, session.ErrInvalidRD) {
					t.Fatalf("ValidateRD(%q) = %+v, %v; want ErrInvalidRD", tt.rd, got, err)
				}
				return
			}
			if err != nil || got.URL != tt.want || got.Host != "app.example.com" {
				t.Fatalf("ValidateRD(%q) = %+v, %v; want %q", tt.rd, got, err, tt.want)
			}
		})
	}
}

func TestValidateRDRegistryFailureIsNotAnInvalidRD(t *testing.T) {
	boom := errors.New("db down")
	svc := newUnitService(t, fakeHosts{err: boom})
	_, err := svc.ValidateRD(context.Background(), "https://app.example.com/")
	if !errors.Is(err, boom) || errors.Is(err, session.ErrInvalidRD) {
		t.Fatalf("err = %v, want the registry error to surface as a failure, not a rejection", err)
	}
}
