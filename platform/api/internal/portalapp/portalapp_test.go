package portalapp_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/portalapp"
	"platform/api/internal/testdb"
)

const (
	testIssuer   = "https://auth.apps.example.test"
	portalAppID  = "app_0000000000000000000000000P"
	otherAppID   = "app_0000000000000000000000000Q"
	helloAppID   = "app_0000000000000000000000000H"
	testKID      = "kid-1"
	portalHost   = "portal.apps.example.test"
	testTimeout  = 15 * time.Second
	assertionTTL = 90 * time.Second
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// stack is a portal over a real database, with a fake platform JWKS and a signer for assertions.
type stack struct {
	t      *testing.T
	owner  *pgxpool.Pool
	portal *pgxpool.Pool
	app    *portalapp.App
	key    *ecdsa.PrivateKey
	jwks   *httptest.Server
}

func newStack(t *testing.T) *stack {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const coordinateSize = 32
	pad := func(b []byte) []byte { return append(make([]byte, coordinateSize-len(b)), b...) }
	x, y := key.X.Bytes(), key.Y.Bytes()
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=300")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "EC", "crv": "P-256", "kid": testKID, "use": "sig", "alg": "ES256",
			"x": base64.RawURLEncoding.EncodeToString(pad(x)), "y": base64.RawURLEncoding.EncodeToString(pad(y)),
		}}})
	}))
	t.Cleanup(jwks.Close)

	owner := testdb.New(t)
	pool := testdb.Portal(t, owner)
	app, err := portalapp.New(portalapp.Config{
		AppID: portalAppID, Issuer: testIssuer, JWKSURL: jwks.URL, Pool: pool, ProbeInterval: 5 * time.Minute, Logger: discardLog,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &stack{t: t, owner: owner, portal: pool, app: app, key: key, jwks: jwks}
}

// assertion signs a Platform-Assertion for sub and aud.
func (s *stack) assertion(sub, aud string) string {
	s.t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": testIssuer, "aud": aud, "sub": sub, "email": "ada@example.com", "name": "Ada", "sid": "s1", "jti": "j1",
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(assertionTTL).Unix(), "roles": []string{},
	})
	tok.Header["kid"] = testKID
	tok.Header["typ"] = "platform-assertion+jwt"
	signed, err := tok.SignedString(s.key)
	if err != nil {
		s.t.Fatal(err)
	}
	return signed
}

func (s *stack) exec(sql string, args ...any) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if _, err := s.owner.Exec(ctx, sql, args...); err != nil {
		s.t.Fatalf("exec %q: %v", sql, err)
	}
}

func scalar[T any](s *stack, sql string, args ...any) T {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	var v T
	if err := s.owner.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		s.t.Fatalf("query %q: %v", sql, err)
	}
	return v
}

type call struct {
	method, path string
	assertion    string
	headers      map[string]string
	body         string
}

// recorder is the response type of the helpers.
type recorder = httptest.ResponseRecorder

func (s *stack) public(c call) *httptest.ResponseRecorder {
	s.t.Helper()
	r := httptest.NewRequestWithContext(s.t.Context(), c.method, "https://"+portalHost+c.path, strings.NewReader(c.body))
	r.Host = portalHost
	if c.assertion != "" {
		r.Header.Set("Platform-Assertion", c.assertion)
	}
	for k, v := range c.headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.app.Public.ServeHTTP(w, r)
	return w
}

// seed creates two initiatives (the platform one with the portal app), a hello app and a user with a grant on it.
func (s *stack) seed() (userID string) {
	s.t.Helper()
	platform := scalar[string](s, `INSERT INTO initiatives (key, name) VALUES ('platform', 'Platform') RETURNING id::text`)
	hello := scalar[string](s, `INSERT INTO initiatives (key, name) VALUES ('hello', 'Hello') RETURNING id::text`)
	s.exec(`INSERT INTO apps (id, initiative_id, name) VALUES ($1, $2, 'Portal')`, portalAppID, platform)
	s.exec(`INSERT INTO apps (id, initiative_id, name) VALUES ($1, $2, 'Hello')`, helloAppID, hello)
	s.exec(`INSERT INTO deployments (host, app_id) VALUES ($1, $2), ('hello.apps.example.test', $3)`, portalHost, portalAppID, helloAppID)
	role := scalar[string](s, `INSERT INTO initiative_roles (initiative_id, key, manages_access) VALUES ($1, 'editor', true) RETURNING id::text`, hello)
	userID = scalar[string](s, `INSERT INTO users (google_sub, email, name) VALUES ('g-1', 'ada@example.com', 'Ada') RETURNING id::text`)
	s.exec(`INSERT INTO initiative_access (initiative_id, role_id, user_id) VALUES ($1, $2, $3)`, hello, role, userID)
	return userID
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

func TestMeEndToEnd(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	uid := s.seed()

	w := s.public(call{method: http.MethodGet, path: "/api/v1/me", assertion: s.assertion(uid, portalAppID), headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	me := decode[struct {
		User struct {
			ID, Email, Name string
		}
		Permissions struct {
			PlatformAdmin      bool                   `json:"platform_admin"`
			ManagedInitiatives []struct{ Key string } `json:"managed_initiatives"`
		}
	}](t, w)
	if me.User.ID != uid || me.User.Email != "ada@example.com" || me.Permissions.PlatformAdmin ||
		len(me.Permissions.ManagedInitiatives) != 1 || me.Permissions.ManagedInitiatives[0].Key != "hello" {
		t.Fatalf("me = %+v", me)
	}

	// A platform admin flag shows up on the next request: nothing is cached or carried in the assertion.
	s.exec(`INSERT INTO platform_admins (user_id) VALUES ($1)`, uid)
	w = s.public(call{method: http.MethodGet, path: "/api/v1/me", assertion: s.assertion(uid, portalAppID)})
	if !strings.Contains(w.Body.String(), `"platform_admin":true`) {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestHomeEndToEnd(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	uid := s.seed()

	w := s.public(call{method: http.MethodGet, path: "/api/v1/me/apps", assertion: s.assertion(uid, portalAppID)})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	home := decode[struct {
		Apps []struct {
			ID    string
			Name  string
			Hosts []string
			Roles []string
		}
	}](t, w)
	// The portal app itself is open to every active user but is not one of "their" apps.
	if len(home.Apps) != 1 || home.Apps[0].ID != helloAppID || home.Apps[0].Hosts[0] != "hello.apps.example.test" || home.Apps[0].Roles[0] != "editor" {
		t.Fatalf("home = %+v", home)
	}
}

func TestDisabledUserWithAValidAssertion(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	uid := s.seed()
	s.exec(`UPDATE users SET status = 'disabled' WHERE id = $1`, uid)

	if w := s.public(call{method: http.MethodGet, path: "/api/v1/me", assertion: s.assertion(uid, portalAppID)}); w.Code != http.StatusForbidden {
		t.Fatalf("me status = %d, want 403", w.Code)
	}
	w := s.public(call{method: http.MethodGet, path: "/api/v1/me/apps", assertion: s.assertion(uid, portalAppID)})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"apps":[]`) {
		t.Fatalf("home = %d %s, want an empty list", w.Code, w.Body.String())
	}
}

func TestAssertionValidation(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	uid := s.seed()
	valid := s.assertion(uid, portalAppID)

	tests := []struct {
		name string
		c    call
		want int
	}{
		{"no assertion", call{method: http.MethodGet, path: "/api/v1/me"}, http.StatusUnauthorized},
		{"garbage assertion", call{method: http.MethodGet, path: "/api/v1/me", assertion: "not.a.jwt"}, http.StatusUnauthorized},
		{"assertion for another app", call{method: http.MethodGet, path: "/api/v1/me", assertion: s.assertion(uid, otherAppID)}, http.StatusUnauthorized},
		{"valid", call{method: http.MethodGet, path: "/api/v1/me", assertion: valid}, http.StatusOK},
		// The guards run first: a sibling app's request is rejected even with a perfectly valid assertion...
		{"valid but same-site", call{method: http.MethodGet, path: "/api/v1/me", assertion: valid, headers: map[string]string{"Sec-Fetch-Site": "same-site"}}, http.StatusForbidden},
		// ...and without one it is still a 403, not a 401: the sibling learns nothing about its assertion.
		{"no assertion and cross-site", call{method: http.MethodGet, path: "/api/v1/me", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}}, http.StatusForbidden},
		{"post from a sibling origin", call{method: http.MethodPost, path: "/api/v1/me", assertion: valid, headers: map[string]string{"Origin": "https://hello.apps.example.test", "Content-Type": "application/json"}, body: "{}"}, http.StatusForbidden},
		{"post as text/plain", call{method: http.MethodPost, path: "/api/v1/me", assertion: valid, headers: map[string]string{"Content-Type": "text/plain"}, body: "x"}, http.StatusUnsupportedMediaType},
		{"post as json reaches the router", call{method: http.MethodPost, path: "/api/v1/me", assertion: valid, headers: map[string]string{"Content-Type": "application/json; charset=utf-8"}, body: "{}"}, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := s.public(tt.c)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.want, w.Body.String())
			}
			for name := range w.Header() {
				if strings.HasPrefix(strings.ToLower(name), "access-control-") {
					t.Errorf("response carries %s", name)
				}
			}
			if w.Header().Get("Strict-Transport-Security") == "" || w.Header().Get("Content-Security-Policy") == "" {
				t.Errorf("security headers missing: %v", w.Header())
			}
		})
	}
}

func TestKeysUnavailableIs503NotAnInvalidAssertion(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	uid := s.seed()
	tok := s.assertion(uid, portalAppID)
	s.jwks.Close()
	if w := s.public(call{method: http.MethodGet, path: "/api/v1/me", assertion: tok}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestInternalListener(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	get := func(path string) int {
		w := httptest.NewRecorder()
		s.app.Internal.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		return w.Code
	}
	if got := get("/healthz"); got != http.StatusOK {
		t.Fatalf("/healthz = %d", got)
	}
	if got := get("/readyz"); got != http.StatusOK {
		t.Fatalf("/readyz = %d", got)
	}
	// The API is not reachable through the internal listener, nor the probes through the public one.
	if got := get("/api/v1/me"); got != http.StatusNotFound {
		t.Fatalf("internal /api/v1/me = %d, want 404", got)
	}
	w := httptest.NewRecorder()
	s.app.Public.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+portalHost+"/readyz", nil))
	if w.Code == http.StatusOK {
		t.Fatal("/readyz must not be served by the public listener")
	}
	s.portal.Close()
	if got := get("/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("/readyz without a database = %d, want 503", got)
	}
}

func TestNewValidatesItsConfig(t *testing.T) {
	t.Parallel()
	if _, err := portalapp.New(portalapp.Config{}); err == nil {
		t.Fatal("accepted an empty config")
	}
	s := newStack(t)
	if _, err := portalapp.New(portalapp.Config{AppID: portalAppID, Issuer: testIssuer, JWKSURL: "not a url", Pool: s.portal}); err == nil {
		t.Fatal("accepted an invalid JWKS URL")
	}
}

// The plain-http warning decides by the parsed scheme: HTTP:// counts, https does not.
func TestNewWarnsOnPlainHTTPJWKS(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	for _, tt := range []struct {
		name, url string
		want      bool
	}{
		{"lowercase http", "http://platform-api.example.test/jwks", true},
		{"uppercase HTTP", "HTTP://platform-api.example.test/jwks", true},
		{"uppercase HTTPS", "HTTPS://platform-api.example.test/jwks", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			_, err := portalapp.New(portalapp.Config{
				AppID: portalAppID, Issuer: testIssuer, JWKSURL: tt.url, Pool: s.portal,
				Logger: slog.New(slog.NewTextHandler(&logs, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(logs.String(), "plain http"); got != tt.want {
				t.Fatalf("warned = %v, want %v; logs: %s", got, tt.want, logs.String())
			}
		})
	}
}

// The SDK exempts one path from validation; the public listener must have none, not even with a catch-all web
// handler behind it.
func TestPublicListenerHasNoUnauthenticatedPath(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	uid := s.seed()
	app, err := portalapp.New(portalapp.Config{
		AppID: portalAppID, Issuer: testIssuer, JWKSURL: s.jwks.URL, Pool: s.portal, Logger: discardLog,
		Web: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/healthz", "/readyz", "/", "/index.html", portalapp.ExemptPath, "/api/v1/me"} {
		for _, assertion := range []string{"", s.assertion(uid, portalAppID)} {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+portalHost+path, nil)
			r.Host = portalHost
			if assertion != "" {
				r.Header.Set("Platform-Assertion", assertion)
			}
			w := httptest.NewRecorder()
			app.Public.ServeHTTP(w, r)
			switch {
			case assertion == "" && w.Code != http.StatusUnauthorized && w.Code != http.StatusNotFound:
				t.Errorf("GET %s without assertion = %d, want 401 or 404", path, w.Code)
			case path == portalapp.ExemptPath && w.Code != http.StatusNotFound:
				t.Errorf("GET %s = %d, want 404 with or without assertion", path, w.Code)
			case assertion != "" && (path == "/healthz" || path == "/readyz") && w.Code != http.StatusNotFound:
				t.Errorf("GET %s with a valid assertion = %d, want 404 (probes are internal-only)", path, w.Code)
			}
		}
	}
}
