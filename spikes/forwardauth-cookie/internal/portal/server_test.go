package portal

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"spike/forwardauth/internal/token"
)

const (
	authHost = "auth.apps.wysted.dev"
	host1    = "app1.apps.wysted.dev"
	host2    = "app2.apps.wysted.dev"
)

// counterReader yields deterministic, never-repeating bytes.
type counterReader struct{ n uint64 }

func (c *counterReader) Read(p []byte) (int, error) {
	for i := range p {
		if i%8 == 0 {
			c.n++
		}
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], c.n)
		p[i] = b[i%8]
	}
	return len(p), nil
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

type env struct {
	srv   *Server
	h     http.Handler
	clock *fakeClock
	key   *ecdsa.PrivateKey
}

func newEnv(t *testing.T) *env {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	srv, err := New(Config{
		AuthHost: authHost,
		Apps:     map[string]AppID{host1: "app1", host2: "app2"},
		Users: map[string]User{
			"alice": {ID: "uuid-alice", Roles: []string{"user"}, Apps: map[AppID]bool{"app1": true}},
			"bob":   {ID: "uuid-bob", Roles: []string{"admin"}, Apps: map[AppID]bool{"app1": true, "app2": true}},
		},
		Key: key, Clock: clock.Now, Rand: &counterReader{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &env{srv: srv, h: srv.Handler(), clock: clock, key: key}
}

func (e *env) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func fwd(host, uri, method string, hdr map[string]string, cookies ...*http.Cookie) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/forwardauth", nil)
	req.Header.Set("X-Forwarded-Host", host)
	req.Header.Set("X-Forwarded-Uri", uri)
	req.Header.Set("X-Forwarded-Method", method)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req
}

// loginCodeFor runs the fake login and returns the one-time code from the 302.
func (e *env) loginCodeFor(t *testing.T, user, rd string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/login?user="+user+"&rd="+url.QueryEscape(rd), nil)
	rec := e.do(req)
	if rec.Code != http.StatusFound {
		t.Fatalf("login status = %d, body %s", rec.Code, rec.Body)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc.Query().Get("code")
}

func (e *env) callback(host, code string) *httptest.ResponseRecorder {
	return e.do(fwd(host, CallbackPath+"?code="+url.QueryEscape(code), http.MethodGet, nil))
}

// appCookie performs login + callback and returns the app cookie.
func (e *env) appCookie(t *testing.T, user, host string) *http.Cookie {
	t.Helper()
	code := e.loginCodeFor(t, user, "https://"+host+"/home")
	rec := e.callback(host, code)
	if rec.Code != http.StatusFound {
		t.Fatalf("callback status = %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == AppCookie {
			return c
		}
	}
	t.Fatal("no app cookie")
	return nil
}

func TestUnauthenticated(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name     string
		uri      string
		method   string
		hdr      map[string]string
		wantCode int
		wantLoc  string
	}{
		{
			name: "html navigation redirects to login with rd", uri: "/dash?x=1", method: http.MethodGet,
			hdr:      map[string]string{"Accept": "text/html,application/xhtml+xml"},
			wantCode: http.StatusFound,
			wantLoc:  "https://" + authHost + "/login?rd=" + url.QueryEscape("https://"+host1+"/dash?x=1"),
		},
		{
			name: "sec-fetch navigate redirects", uri: "/", method: http.MethodGet,
			hdr:      map[string]string{"Sec-Fetch-Mode": "navigate"},
			wantCode: http.StatusFound,
			wantLoc:  "https://" + authHost + "/login?rd=" + url.QueryEscape("https://"+host1+"/"),
		},
		{name: "xhr json accept", uri: "/data", method: http.MethodGet,
			hdr: map[string]string{"Accept": "application/json"}, wantCode: http.StatusUnauthorized},
		{name: "x-requested-with", uri: "/data", method: http.MethodGet,
			hdr: map[string]string{"X-Requested-With": "XMLHttpRequest", "Accept": "text/html"}, wantCode: http.StatusUnauthorized},
		{name: "api path even with html accept", uri: "/api/items", method: http.MethodGet,
			hdr: map[string]string{"Accept": "text/html"}, wantCode: http.StatusUnauthorized},
		{name: "html POST is not redirected", uri: "/form", method: http.MethodPost,
			hdr: map[string]string{"Accept": "text/html"}, wantCode: http.StatusUnauthorized},
		{name: "no accept hints", uri: "/", method: http.MethodGet, wantCode: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.do(fwd(host1, tt.uri, tt.method, tt.hdr))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if got := rec.Header().Get("Location"); got != tt.wantLoc {
				t.Fatalf("Location = %q, want %q", got, tt.wantLoc)
			}
			if tt.wantCode == http.StatusUnauthorized && rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
			}
			if rec.Header().Get(token.HeaderName) != "" {
				t.Fatal("token must not be set")
			}
		})
	}
}

func TestPreflight(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name     string
		hdr      map[string]string
		wantCode int
	}{
		{"real preflight passes", map[string]string{"Origin": "https://" + host2, "Access-Control-Request-Method": "POST"}, http.StatusOK},
		{"options without ACRM is not a preflight", map[string]string{"Origin": "https://" + host2}, http.StatusUnauthorized},
		{"options without origin is not a preflight", map[string]string{"Access-Control-Request-Method": "POST"}, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.do(fwd(host1, "/api/x", http.MethodOptions, tt.hdr))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if rec.Header().Get(token.HeaderName) != "" {
				t.Fatal("preflight must not receive a token")
			}
		})
	}
}

func TestHostDerivation(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name     string
		host     string
		hdr      map[string]string
		wantCode int
	}{
		{"unknown host", "evil.example.com", nil, http.StatusForbidden},
		{"empty host", "", nil, http.StatusForbidden},
		{"host with port is not an exact match", host1 + ":8443", nil, http.StatusForbidden},
		{"prefix header does not change decision", host1, map[string]string{"X-Forwarded-Prefix": "/app2"}, http.StatusUnauthorized},
		{"port and proto headers ignored", host1, map[string]string{"X-Forwarded-Port": "80", "X-Forwarded-Proto": "http"}, http.StatusUnauthorized},
		{"prefix on unknown host stays forbidden", "evil.example.com", map[string]string{"X-Forwarded-Prefix": "/" + host1}, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.do(fwd(tt.host, "/x", http.MethodGet, tt.hdr))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
		})
	}
}

func TestCallbackIssuesHostCookie(t *testing.T) {
	e := newEnv(t)
	code := e.loginCodeFor(t, "alice", "https://"+host1+"/home?a=b")
	rec := e.callback(host1, code)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "https://"+host1+"/home?a=b" {
		t.Fatalf("Location = %q", got)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != "__Host-portal_app" || c.Path != "/" || c.Domain != "" ||
		!c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Value == "" {
		t.Fatalf("bad cookie attributes: %+v", c)
	}
	if raw := rec.Header().Get("Set-Cookie"); raw == "" || strings.Contains(strings.ToLower(raw), "domain=") {
		t.Fatalf("raw Set-Cookie must not carry Domain: %q", raw)
	}
}

func TestCodeRejection(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, e *env) *httptest.ResponseRecorder
	}{
		{"reused code", func(t *testing.T, e *env) *httptest.ResponseRecorder {
			code := e.loginCodeFor(t, "bob", "https://"+host1+"/")
			if first := e.callback(host1, code); first.Code != http.StatusFound {
				t.Fatalf("first use = %d", first.Code)
			}
			return e.callback(host1, code)
		}},
		{"expired code", func(t *testing.T, e *env) *httptest.ResponseRecorder {
			code := e.loginCodeFor(t, "bob", "https://"+host1+"/")
			e.clock.Advance(CodeTTL + time.Second)
			return e.callback(host1, code)
		}},
		{"code from another host", func(t *testing.T, e *env) *httptest.ResponseRecorder {
			code := e.loginCodeFor(t, "bob", "https://"+host1+"/")
			return e.callback(host2, code)
		}},
		{"unknown code", func(t *testing.T, e *env) *httptest.ResponseRecorder {
			return e.callback(host1, "nope")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			rec := tt.run(t, e)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Fatal("no cookie may be issued")
			}
		})
	}

	t.Run("wrong-host attempt burns the code", func(t *testing.T) {
		e := newEnv(t)
		code := e.loginCodeFor(t, "bob", "https://"+host1+"/")
		e.callback(host2, code)
		if rec := e.callback(host1, code); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
}

func TestAuthorizedAndForbidden(t *testing.T) {
	tests := []struct {
		name     string
		user     string
		host     string
		wantCode int
		wantAud  string
	}{
		{"alice on app1", "alice", host1, http.StatusOK, "app1"},
		{"bob on app1", "bob", host1, http.StatusOK, "app1"},
		{"bob on app2", "bob", host2, http.StatusOK, "app2"},
		{"alice on app2 is forbidden", "alice", host2, http.StatusForbidden, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			cookie := e.appCookie(t, tt.user, tt.host)
			rec := e.do(fwd(tt.host, "/x", http.MethodGet, nil, cookie))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			raw := rec.Header().Get(token.HeaderName)
			if tt.wantCode != http.StatusOK {
				if raw != "" {
					t.Fatal("token must not be set on denial")
				}
				return
			}
			claims := &token.Claims{}
			_, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return &e.key.PublicKey, nil },
				jwt.WithValidMethods([]string{"ES256"}), jwt.WithAudience(tt.wantAud), jwt.WithTimeFunc(e.clock.Now))
			if err != nil {
				t.Fatalf("parse token: %v", err)
			}
			if claims.ExpiresAt.Sub(claims.IssuedAt.Time) != 90*time.Second {
				t.Fatalf("ttl = %v", claims.ExpiresAt.Sub(claims.IssuedAt.Time))
			}
			if claims.Subject != "uuid-"+tt.user {
				t.Fatalf("sub = %q", claims.Subject)
			}
		})
	}
}

func TestAppCookieIsPerApp(t *testing.T) {
	e := newEnv(t)
	cookie := e.appCookie(t, "bob", host1)
	// Same cookie value replayed against another app host must not authenticate.
	rec := e.do(fwd(host2, "/x", http.MethodGet, map[string]string{"Accept": "application/json"}, cookie))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAppSessionExpires(t *testing.T) {
	e := newEnv(t)
	cookie := e.appCookie(t, "bob", host1)
	e.clock.Advance(AppSessionTTL + time.Second)
	rec := e.do(fwd(host1, "/x", http.MethodGet, map[string]string{"Accept": "application/json"}, cookie))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestLoginRD(t *testing.T) {
	tests := []struct {
		name     string
		rd       string
		wantCode int
	}{
		{"known https host", "https://" + host1 + "/p", http.StatusFound},
		{"external host", "https://evil.example.com/", http.StatusBadRequest},
		{"http scheme", "http://" + host1 + "/", http.StatusBadRequest},
		{"auth host is not an app", "https://" + authHost + "/", http.StatusBadRequest},
		{"userinfo trick", "https://" + host1 + "@evil.example.com/", http.StatusBadRequest},
		{"scheme-relative", "//evil.example.com/", http.StatusBadRequest},
		{"relative path", "/foo", http.StatusBadRequest},
		{"javascript scheme", "javascript:alert(1)", http.StatusBadRequest},
		{"empty", "", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.do(httptest.NewRequest(http.MethodGet, "/login?user=alice&rd="+url.QueryEscape(tt.rd), nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if tt.wantCode != http.StatusFound && len(rec.Result().Cookies()) != 0 {
				t.Fatal("rejected rd must not create a session")
			}
		})
	}
}

func TestLoginSessionCookieAndSSO(t *testing.T) {
	e := newEnv(t)
	rec := e.do(httptest.NewRequest(http.MethodGet, "/login?user=bob&rd="+url.QueryEscape("https://"+host1+"/"), nil))
	var sess *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			sess = c
		}
	}
	if sess == nil || sess.Domain != "" || sess.Path != "/" || !sess.Secure || !sess.HttpOnly {
		t.Fatalf("bad session cookie: %+v", sess)
	}
	// Second app: no ?user, the central session is reused.
	req := httptest.NewRequest(http.MethodGet, "/login?rd="+url.QueryEscape("https://"+host2+"/"), nil)
	req.AddCookie(sess)
	rec = e.do(req)
	if rec.Code != http.StatusFound {
		t.Fatalf("sso status = %d", rec.Code)
	}
	// Without session: chooser, not a redirect.
	rec = e.do(httptest.NewRequest(http.MethodGet, "/login?rd="+url.QueryEscape("https://"+host2+"/"), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("chooser status = %d", rec.Code)
	}
}

func TestJWKS(t *testing.T) {
	e := newEnv(t)
	rec := e.do(httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(e.srv.jwks.Keys) != 1 {
		t.Fatal("expected one key")
	}
	pub, err := e.srv.jwks.Keys[0].PublicKey()
	if err != nil || !pub.Equal(&e.key.PublicKey) {
		t.Fatalf("jwk does not round-trip: %v", err)
	}
}

func TestForwardAuthAuthHost(t *testing.T) {
	cases := []struct {
		name       string
		host       string
		wantStatus int
	}{
		{"auth host", authHost, http.StatusOK},
		{"auth host uppercase", strings.ToUpper(authHost), http.StatusOK},
		{"unknown host", "evil.example.com", http.StatusForbidden},
		{"empty host", "", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			rec := e.do(fwd(tc.host, "/login", http.MethodGet, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get(token.HeaderName); got != "" {
				t.Fatalf("%s must be empty, got a value", token.HeaderName)
			}
		})
	}
}

func TestNewRejectsInvalidAuthHost(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		authHost string
		apps     map[string]AppID
	}{
		{"auth host in apps", authHost, map[string]AppID{host1: "app1", authHost: "portal"}},
		{"auth host in apps, different case", authHost, map[string]AppID{strings.ToUpper(authHost): "portal"}},
		{"empty auth host", "", map[string]AppID{host1: "app1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{AuthHost: tc.authHost, Apps: tc.apps, Key: key})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}
