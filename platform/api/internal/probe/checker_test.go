package probe

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	// testHost is the name in the certificate of httptest.NewTLSServer: the probe verifies it for real.
	testHost = "example.com"
	authHost = "auth.apps.example.test"
	loginURL = "https://" + authHost + "/login?rd=https%3A%2F%2Fexample.com%2F&bind=abc"
)

// entrypoint is a TLS server standing in for Traefik: the checker dials it whatever host it probes.
type entrypoint struct {
	srv  *httptest.Server
	pool *x509.CertPool
}

func newEntrypoint(t *testing.T, h http.Handler) entrypoint {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return entrypoint{srv: srv, pool: pool}
}

func (e entrypoint) checker(t *testing.T, mutate func(*CheckerConfig)) *Checker {
	t.Helper()
	cfg := CheckerConfig{AuthHost: authHost, Addr: e.srv.Listener.Addr().String(), RootCAs: e.pool, Timeout: 5 * time.Second}
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := NewChecker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func respond(status int, location string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if location != "" {
			w.Header().Set("Location", location)
		}
		w.WriteHeader(status)
	})
}

func TestClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		status   int
		location string
		want     Status
	}{
		{"302 to the login", http.StatusFound, loginURL, StatusOK},
		{"303 to the login", http.StatusSeeOther, loginURL, StatusOK},
		{"307 to the login", http.StatusTemporaryRedirect, loginURL, StatusOK},
		{"login host in another case", http.StatusFound, "https://AUTH.apps.example.test/login", StatusOK},
		{"301 to the login", http.StatusMovedPermanently, loginURL, StatusUnexpected},
		{"308 to the login", http.StatusPermanentRedirect, loginURL, StatusUnexpected},
		{"redirect to another host", http.StatusFound, "https://evil.example.test/login", StatusUnexpected},
		{"redirect to another path of the auth host", http.StatusFound, "https://" + authHost + "/other", StatusUnexpected},
		{"redirect to the auth host root", http.StatusFound, "https://" + authHost + "/", StatusUnexpected},
		{"redirect to the login over http", http.StatusFound, "http://" + authHost + "/login", StatusUnexpected},
		{"redirect to the login of a lookalike host", http.StatusFound, "https://" + authHost + ".evil.test/login", StatusUnexpected},
		{"redirect with credentials", http.StatusFound, "https://user@" + authHost + "/login", StatusUnexpected},
		{"relative redirect", http.StatusFound, "/login", StatusUnexpected},
		{"redirect to itself", http.StatusFound, "https://" + testHost + "/", StatusUnexpected},
		{"redirect without location", http.StatusFound, "", StatusUnexpected},
		{"200", http.StatusOK, "", StatusUnprotected},
		{"200 with a location header", http.StatusOK, loginURL, StatusUnprotected},
		{"204", http.StatusNoContent, "", StatusUnprotected},
		{"304", http.StatusNotModified, "", StatusUnprotected},
		{"401 as an API answer", http.StatusUnauthorized, "", StatusUnprotected},
		{"403", http.StatusForbidden, "", StatusUnprotected},
		{"404", http.StatusNotFound, "", StatusUnprotected},
		{"500", http.StatusInternalServerError, "", StatusUnprotected},
		{"503", http.StatusServiceUnavailable, "", StatusUnprotected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEntrypoint(t, respond(tt.status, tt.location))
			got := e.checker(t, nil).Check(context.Background(), Target{Host: testHost, AppID: "app_x"})
			if got.Status != tt.want || got.Code != tt.status || got.Host != testHost || got.AppID != "app_x" {
				t.Fatalf("result = %+v, want status %q code %d", got, tt.want, tt.status)
			}
		})
	}
}

func TestAuthHostWithPort(t *testing.T) {
	t.Parallel()
	e := newEntrypoint(t, respond(http.StatusFound, "https://auth.example.test:8443/login"))
	c := e.checker(t, func(c *CheckerConfig) { c.AuthHost = "auth.example.test:8443" })
	if got := c.Check(context.Background(), Target{Host: testHost}); got.Status != StatusOK {
		t.Fatalf("result = %+v", got)
	}
	c = e.checker(t, nil) // the configured auth host has no port
	if got := c.Check(context.Background(), Target{Host: testHost}); got.Status != StatusUnexpected {
		t.Fatalf("a port mismatch must not match: %+v", got)
	}
}

// The probe behaves like a cookie-less browser navigation and nothing else.
func TestRequestShape(t *testing.T) {
	t.Parallel()
	var (
		mu   sync.Mutex
		seen []*http.Request
	)
	e := newEntrypoint(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Clone(context.Background()))
		mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "__Host-Http-platform_bind", Value: "x", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		w.Header().Set("Location", loginURL)
		w.WriteHeader(http.StatusFound)
	}))
	c := e.checker(t, nil)
	for range 2 {
		if got := c.Check(context.Background(), Target{Host: testHost}); got.Status != StatusOK {
			t.Fatalf("result = %+v", got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("%d requests for 2 probes", len(seen))
	}
	for i, r := range seen {
		switch {
		case r.Method != http.MethodGet || r.URL.Path != "/":
			t.Errorf("request %d = %s %s", i, r.Method, r.URL.Path)
		case r.Host != testHost:
			t.Errorf("request %d Host = %q, want the deployment host", i, r.Host)
		case r.Header.Get("Accept") != "text/html" || r.Header.Get("Sec-Fetch-Mode") != "navigate":
			t.Errorf("request %d lacks the navigation hints: %v", i, r.Header)
		case r.Header.Get("Cookie") != "":
			t.Errorf("request %d carries cookies: %q", i, r.Header.Get("Cookie"))
		case r.Header.Get("Authorization") != "":
			t.Errorf("request %d carries credentials", i)
		case r.TLS == nil || r.TLS.ServerName != testHost:
			t.Errorf("request %d SNI = %+v, want the deployment host", i, r.TLS)
		}
	}
}

// A redirect is the answer: following it would make every protected host look like the auth host's login page.
func TestRedirectIsNotFollowed(t *testing.T) {
	t.Parallel()
	var loginHits atomic.Int32
	e := newEntrypoint(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			loginHits.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	got := e.checker(t, nil).Check(context.Background(), Target{Host: testHost})
	if loginHits.Load() != 0 || got.Status != StatusUnexpected || got.Code != http.StatusFound {
		t.Fatalf("hits %d, result %+v: the redirect was followed", loginHits.Load(), got)
	}
}

func TestUnreachable(t *testing.T) {
	t.Parallel()
	t.Run("nothing listens", func(t *testing.T) {
		t.Parallel()
		e := newEntrypoint(t, respond(http.StatusOK, ""))
		c := e.checker(t, nil)
		e.srv.Close()
		if got := c.Check(context.Background(), Target{Host: testHost}); got.Status != StatusUnreachable || got.Code != 0 {
			t.Fatalf("result = %+v", got)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		e := newEntrypoint(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		t.Cleanup(func() { close(release) })
		c := e.checker(t, func(c *CheckerConfig) { c.Timeout = 150 * time.Millisecond })
		start := time.Now()
		got := c.Check(context.Background(), Target{Host: testHost})
		if got.Status != StatusUnreachable || time.Since(start) > 3*time.Second {
			t.Fatalf("result = %+v after %s", got, time.Since(start))
		}
	})
	t.Run("certificate not trusted", func(t *testing.T) {
		t.Parallel()
		e := newEntrypoint(t, respond(http.StatusOK, ""))
		c := e.checker(t, func(c *CheckerConfig) { c.RootCAs = nil })
		if got := c.Check(context.Background(), Target{Host: testHost}); got.Status != StatusUnreachable {
			t.Fatalf("an untrusted certificate answered %+v", got)
		}
	})
	t.Run("certificate for another name", func(t *testing.T) {
		t.Parallel()
		e := newEntrypoint(t, respond(http.StatusOK, ""))
		if got := e.checker(t, nil).Check(context.Background(), Target{Host: "other.apps.example.test"}); got.Status != StatusUnreachable {
			t.Fatalf("a name mismatch answered %+v", got)
		}
	})
	t.Run("insecure flag skips the verification", func(t *testing.T) {
		t.Parallel()
		e := newEntrypoint(t, respond(http.StatusFound, loginURL))
		c := e.checker(t, func(c *CheckerConfig) { c.RootCAs, c.InsecureSkipVerify = nil, true })
		if got := c.Check(context.Background(), Target{Host: "other.apps.example.test"}); got.Status != StatusOK {
			t.Fatalf("result = %+v", got)
		}
	})
	t.Run("canceled context", func(t *testing.T) {
		t.Parallel()
		e := newEntrypoint(t, respond(http.StatusOK, ""))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := e.checker(t, nil).Check(ctx, Target{Host: testHost}); got.Status != StatusUnreachable {
			t.Fatalf("result = %+v", got)
		}
	})
}

func TestNewCheckerValidation(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]CheckerConfig{
		"no auth host":         {},
		"address without port": {AuthHost: authHost, Addr: "traefik.example.test"},
		"address empty port":   {AuthHost: authHost, Addr: "traefik.example.test:"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewChecker(cfg); err == nil {
				t.Fatal("NewChecker accepted the configuration")
			}
		})
	}
	if _, err := NewChecker(CheckerConfig{AuthHost: authHost, Addr: "traefik.traefik.svc:443"}); err != nil {
		t.Fatal(err)
	}
}

// Without a dial address the probe resolves the deployment host itself (port 443).
func TestDefaultDialUsesTheDeploymentHost(t *testing.T) {
	t.Parallel()
	c, err := NewChecker(CheckerConfig{AuthHost: authHost, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	// ".invalid" never resolves (RFC 6761): unreachable without touching the network.
	if got := c.Check(context.Background(), Target{Host: "nothing.invalid"}); got.Status != StatusUnreachable {
		t.Fatalf("result = %+v", got)
	}
}
