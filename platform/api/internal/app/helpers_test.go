package app_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/access"
	"platform/api/internal/app"
	"platform/api/internal/forwardauth"
	"platform/api/internal/jwks"
	"platform/api/internal/keys"
	"platform/api/internal/postgres"
	"platform/api/internal/session"
	"platform/api/internal/testdb"
	platformauth "platform/sdk/go"
)

const (
	authHost   = "auth.apps.example.test"
	issuerURL  = "https://" + authHost
	hostA      = "a.apps.example.test"
	hostB      = "b.apps.example.test"
	hostC      = "c.apps.example.test"
	appIDA     = "app_0000000000000000000000000A"
	appIDB     = "app_0000000000000000000000000B"
	appIDC     = "app_0000000000000000000000000C"
	testTO     = 10 * time.Second
	pollWindow = 5 * time.Second // must stay well under the 30 s decision cache TTL
)

var (
	navHeaders = map[string]string{"Accept": "text/html,application/xhtml+xml", "Sec-Fetch-Mode": "navigate"}
	apiHeaders = map[string]string{"Accept": "application/json", "Sec-Fetch-Mode": "cors"}
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type envOptions struct {
	noFakeLogin bool
	ringKEK     *keys.KEK // keyring KEK different from the one that sealed the keys
	noReload    bool      // never load the keyring

	requestTimeout time.Duration // per-request deadline; zero is the default
}

type env struct {
	t        *testing.T
	pool     *pgxpool.Pool // owner role
	rt       *pgxpool.Pool // runtime role, what the app uses
	ring     *keys.Keyring
	app      *app.App
	internal *httptest.Server
	public   *httptest.Server
	logs     *syncBuffer
	client   *http.Client

	initiativeID string
	roleID       string
	otherInitID  string
}

func newEnv(t *testing.T, opts envOptions) *env {
	t.Helper()
	pool := testdb.New(t)         // owner role: fixtures and assertions only
	rt := testdb.Runtime(t, pool) // what the server runs on, exactly as "serve" does
	kek, _, err := keys.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	store := postgres.NewKeyStore(rt)
	rotator, err := keys.NewRotator(keys.RotatorConfig{Store: store, KEK: kek})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTO)
	defer cancel()
	if _, err := rotator.Tick(ctx); err != nil {
		t.Fatalf("rotation tick: %v", err)
	}
	ringKEK := kek
	if opts.ringKEK != nil {
		ringKEK = opts.ringKEK
	}
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	ring, err := keys.NewKeyring(keys.KeyringConfig{Store: store, KEK: ringKEK, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.noReload {
		_ = ring.Reload(ctx) // a KEK mismatch is reported as an error and is part of the scenario
	}

	a, err := app.New(app.Config{
		AuthHost: authHost, Issuer: issuerURL, FakeLogin: !opts.noFakeLogin, Pool: rt, Keyring: ring, Logger: log,
		RequestTimeout: opts.requestTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, pool: pool, rt: rt, ring: ring, app: a, logs: logs}
	e.internal = httptest.NewServer(a.Internal)
	e.public = httptest.NewServer(a.Public)
	e.client = &http.Client{
		Timeout:       testTO,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	t.Cleanup(e.internal.Close)
	t.Cleanup(e.public.Close)

	listenCtx, stop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		postgres.Listen(listenCtx, rt, access.NotifyChannel, a.InvalidateAccess, log)
	}()
	t.Cleanup(func() { stop(); wg.Wait() })

	e.seed()
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTO)
	defer cancel()
	if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
		e.t.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *env) scalar(sql string, args ...any) string {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTO)
	defer cancel()
	var out string
	if err := e.pool.QueryRow(ctx, sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("query %q: %v", sql, err)
	}
	return out
}

// seed creates initiative alpha (role editor; apps A and B) and initiative beta (app C).
func (e *env) seed() {
	e.initiativeID = e.scalar(`INSERT INTO initiatives (key) VALUES ('alpha') RETURNING id::text`)
	e.roleID = e.scalar(`INSERT INTO initiative_roles (initiative_id, key) VALUES ($1, 'editor') RETURNING id::text`, e.initiativeID)
	e.otherInitID = e.scalar(`INSERT INTO initiatives (key) VALUES ('beta') RETURNING id::text`)
	for _, a := range []struct{ id, host, initiative string }{
		{appIDA, hostA, e.initiativeID}, {appIDB, hostB, e.initiativeID}, {appIDC, hostC, e.otherInitID},
	} {
		e.exec(`INSERT INTO apps (id, initiative_id) VALUES ($1, $2)`, a.id, a.initiative)
		e.exec(`INSERT INTO deployments (host, app_id) VALUES ($1, $2)`, a.host, a.id)
	}
}

func (e *env) userID(email string) string {
	e.t.Helper()
	return e.scalar(`SELECT id::text FROM users WHERE google_sub = $1`, "fake:"+email)
}

// grant gives the (already provisioned) user the editor role on alpha.
func (e *env) grant(email string) {
	e.t.Helper()
	e.exec(`INSERT INTO initiative_access (initiative_id, role_id, user_id) VALUES ($1, $2, $3)`,
		e.initiativeID, e.roleID, e.userID(email))
}

func (e *env) revokeGrant(email string) {
	e.t.Helper()
	e.exec(`DELETE FROM initiative_access WHERE user_id = $1`, e.userID(email))
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTO)
	defer cancel()
	var n int
	if err := e.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func (e *env) verifier(audience string) *platformauth.Verifier {
	e.t.Helper()
	v, err := platformauth.NewVerifier(platformauth.Config{Issuer: issuerURL, Audience: audience, JWKSURL: e.internal.URL + jwks.Path})
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

// result is a response as the browser (or Traefik) sees it.
type result struct {
	status  int
	header  http.Header
	body    string
	cookies []*http.Cookie
}

func (r result) location() string { return r.header.Get("Location") }

func (r result) cookie(name string) *http.Cookie {
	for _, c := range r.cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// browser keeps a cookie jar per host and plays Traefik: it calls /forwardauth for app hosts and the public
// listener for the auth host. As Traefik does, cookies are applied only when the auth answer is not 2xx.
type browser struct {
	t   *testing.T
	env *env
	jar map[string]map[string]string
}

func (e *env) newBrowser() *browser {
	return &browser{t: e.t, env: e, jar: map[string]map[string]string{}}
}

func (b *browser) cookie(host, name string) string { return b.jar[host][name] }

func (b *browser) apply(host string, cookies []*http.Cookie) {
	for _, c := range cookies {
		if b.jar[host] == nil {
			b.jar[host] = map[string]string{}
		}
		if c.MaxAge < 0 {
			delete(b.jar[host], c.Name)
			continue
		}
		b.jar[host][c.Name] = c.Value
	}
}

func (b *browser) cookieHeader(host string) string {
	var parts []string
	for n, v := range b.jar[host] {
		parts = append(parts, n+"="+v)
	}
	return strings.Join(parts, "; ")
}

func (b *browser) do(req *http.Request) result {
	b.t.Helper()
	resp, err := b.env.client.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return result{status: resp.StatusCode, header: resp.Header, body: string(body), cookies: resp.Cookies()}
}

type fwd struct {
	host, method, uri string
	headers           map[string]string
	noCookies         bool
}

// forward asks /forwardauth about a request to an app host and enforces the invariants of every answer.
func (b *browser) forward(f fwd) result {
	b.t.Helper()
	if f.method == "" {
		f.method = http.MethodGet
	}
	if f.uri == "" {
		f.uri = "/"
	}
	req, _ := http.NewRequest(http.MethodGet, b.env.internal.URL+"/forwardauth", nil) // Traefik always uses GET
	req.Header.Set("X-Forwarded-Host", f.host)
	req.Header.Set("X-Forwarded-Method", f.method)
	req.Header.Set("X-Forwarded-Uri", f.uri)
	req.Header.Set("X-Forwarded-Proto", "http") // must be ignored
	req.Header.Set("X-Forwarded-Port", "80")
	req.Header.Set("X-Forwarded-Prefix", "/ignored")
	for k, v := range f.headers {
		req.Header.Set(k, v)
	}
	if h := b.cookieHeader(f.host); h != "" && !f.noCookies {
		req.Header.Set("Cookie", h)
	}
	res := b.do(req)
	if got := res.header.Get("Cache-Control"); got != "no-store" {
		b.t.Errorf("%s %s%s: Cache-Control = %q, want no-store", f.method, f.host, f.uri, got)
	}
	if res.status != http.StatusOK && res.header.Get(forwardauth.HeaderAssertion) != "" {
		b.t.Errorf("%s %s%s: %d response carries %s", f.method, f.host, f.uri, res.status, forwardauth.HeaderAssertion)
	}
	if res.status != http.StatusOK {
		b.apply(f.host, res.cookies)
	}
	return res
}

// auth sends a request to the public listener as the browser would for the auth host.
func (b *browser) auth(method, target string, form url.Values, headers map[string]string) result {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, b.env.public.URL+target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if h := b.cookieHeader(authHost); h != "" {
		req.Header.Set("Cookie", h)
	}
	res := b.do(req)
	b.apply(authHost, res.cookies)
	return res
}

var sameOrigin = map[string]string{"Origin": issuerURL}

// login drives the whole flow from a navigation to host/uri to the final redirect with the app cookie set.
// It returns the response of the callback (302 to rd). If the browser has a central session no form is shown.
func (b *browser) login(host, uri, email string) result {
	b.t.Helper()
	first := b.forward(fwd{host: host, uri: uri, headers: navHeaders})
	if first.status != http.StatusFound {
		b.t.Fatalf("navigation without cookie: status %d, want 302", first.status)
	}
	loginURL, err := url.Parse(first.location())
	if err != nil || loginURL.Host != authHost {
		b.t.Fatalf("login redirect = %q", first.location())
	}
	page := b.auth(http.MethodGet, loginURL.RequestURI(), nil, nil)
	var callback result
	switch page.status {
	case http.StatusFound: // SSO: a central session exists
		callback = page
	case http.StatusOK:
		q := loginURL.Query()
		callback = b.auth(http.MethodPost, "/login", url.Values{"email": {email}, "rd": {q.Get("rd")}, "bind": {q.Get("bind")}}, sameOrigin)
		if callback.status != http.StatusFound {
			b.t.Fatalf("fake login POST: status %d, body %q", callback.status, callback.body)
		}
	default:
		b.t.Fatalf("login page: status %d, body %q", page.status, page.body)
	}
	return b.followCallback(callback)
}

func (b *browser) followCallback(r result) result {
	b.t.Helper()
	cb, err := url.Parse(r.location())
	if err != nil || cb.Host == authHost {
		b.t.Fatalf("callback redirect = %q", r.location())
	}
	if got := r.header.Get("Referrer-Policy"); got != "no-referrer" {
		b.t.Errorf("login redirect Referrer-Policy = %q", got)
	}
	return b.forward(fwd{host: cb.Host, uri: cb.RequestURI(), headers: navHeaders})
}

// bindHash returns base64url(sha256(bind cookie value)) as the login URL must carry it.
func bindHash(t *testing.T, value string) string {
	t.Helper()
	h, ok := session.HashToken(value)
	if !ok {
		t.Fatalf("malformed bind cookie value")
	}
	return base64.RawURLEncoding.EncodeToString(h)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(pollWindow)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
