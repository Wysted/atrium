package app_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Wysted/atrium/platform/api/internal/session"
)

var ipCounter atomic.Int64

// gflow is one Google login attempt of a browser, step by step, so tests can tamper between steps.
// Every flow comes from its own client address: the rate limit is not what these tests are about.
type gflow struct {
	t       *testing.T
	e       *env
	b       *browser
	ip      string
	loginQ  url.Values // rd and bind of the /login URL
	login   result     // response of GET /login
	cbURL   *url.URL   // where the provider sent the browser
	cookie  string     // value of the login cookie
	reqHost string
}

func (f *gflow) auth(target string) result {
	f.t.Helper()
	return f.b.auth(http.MethodGet, target, nil, map[string]string{"X-Forwarded-For": f.ip})
}

// beginGoogle drives: navigation to host/uri -> 302 to /login -> GET /login.
func (e *env) beginGoogle(b *browser, host, uri string) *gflow {
	e.t.Helper()
	first := b.forward(fwd{host: host, uri: uri, headers: navHeaders})
	if first.status != http.StatusFound {
		e.t.Fatalf("navigation without cookie: status %d", first.status)
	}
	loginURL, err := url.Parse(first.location())
	if err != nil || loginURL.Host != authHost {
		e.t.Fatalf("login redirect = %q", first.location())
	}
	f := &gflow{t: e.t, e: e, b: b, ip: fmt.Sprintf("198.51.100.%d", ipCounter.Add(1)%250+1), loginQ: loginURL.Query(), reqHost: host}
	f.login = f.auth(loginURL.RequestURI())
	return f
}

// provider follows the 302 to the provider (what the browser does) and records the redirect back to the callback.
func (f *gflow) provider() {
	f.t.Helper()
	if f.login.status != http.StatusFound {
		f.t.Fatalf("GET /login: %d %q", f.login.status, f.login.body)
	}
	f.cookie = f.b.cookie(authHost, session.CookieLogin)
	req, err := http.NewRequestWithContext(f.t.Context(), http.MethodGet, f.login.location(), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	resp, err := f.e.client.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		f.t.Fatalf("provider authorize: %d", resp.StatusCode)
	}
	u, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || u.Scheme != "https" || u.Host != authHost || u.Path != "/oauth2/callback" {
		f.t.Fatalf("provider redirect = %q", resp.Header.Get("Location"))
	}
	f.cbURL = u
}

// callback calls /oauth2/callback with the provider's parameters after mutate changed them.
func (f *gflow) callback(mutate func(url.Values)) result {
	f.t.Helper()
	q := f.cbURL.Query()
	if mutate != nil {
		mutate(q)
	}
	return f.auth("/oauth2/callback?" + q.Encode())
}

// googleLogin runs the whole flow and returns the app-side callback result (302 to rd with the app cookie).
func (e *env) googleLogin(b *browser, host, uri string) result {
	e.t.Helper()
	f := e.beginGoogle(b, host, uri)
	f.provider()
	cb := f.callback(nil)
	if cb.status != http.StatusFound {
		e.t.Fatalf("callback: %d %q", cb.status, cb.body)
	}
	return b.followCallback(cb)
}

func (e *env) userIDBySub(sub string) string {
	e.t.Helper()
	return e.scalar(`SELECT id::text FROM users WHERE google_sub = $1`, sub)
}

func (e *env) grantUser(userID string) {
	e.t.Helper()
	e.exec(`INSERT INTO initiative_access (initiative_id, role_id, user_id) VALUES ($1, $2, $3)`, e.initiativeID, e.roleID, userID)
}

// logRecords returns the parsed log lines whose msg is msg.
func (e *env) logRecords(msg string) []map[string]any {
	var out []map[string]any
	for line := range strings.SplitSeq(e.logs.String(), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// dumpTables is the text of every table a login writes to.
func (e *env) dumpTables() string {
	var sb strings.Builder
	for _, tbl := range []string{"oauth_flows", "sessions", "app_sessions", "login_codes", "users", "audit_events", "access_denials"} {
		sb.WriteString(e.scalar(fmt.Sprintf(`SELECT coalesce(string_agg(t::text, ' '), '') FROM %s t`, tbl)))
		sb.WriteString(" ")
	}
	return sb.String()
}

func (f *gflow) flowBurned() bool {
	f.t.Helper()
	hash, ok := session.HashToken(f.cookie)
	if !ok {
		f.t.Fatal("no login cookie recorded")
	}
	return f.e.count(`SELECT count(*) FROM oauth_flows WHERE token_hash = $1 AND consumed_at IS NOT NULL`, hash) == 1
}

func TestGoogleLoginFullFlow(t *testing.T) {
	fg := newFakeGoogle(t)
	e := newEnv(t, envOptions{google: fg})
	b := e.newBrowser()
	const sub = "100000000000000000001"

	f := e.beginGoogle(b, hostA, "/reports?id=7")
	if f.login.status != http.StatusFound {
		t.Fatalf("GET /login: %d %q", f.login.status, f.login.body)
	}
	c := f.login.cookie(session.CookieLogin)
	if c == nil || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" || c.MaxAge != int(10*time.Minute/time.Second) {
		t.Fatalf("login cookie = %+v", c)
	}
	if f.login.header.Get("Referrer-Policy") != "no-referrer" || f.login.header.Get("Cache-Control") != "no-store" {
		t.Errorf("login redirect headers: %v", f.login.header)
	}
	if f.login.cookie(session.CookieSession) != nil {
		t.Fatal("a session was created before the user authenticated")
	}

	// The authorization request carries everything the contract asks for.
	f.provider()
	q := fg.lastAuth()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": googleClientID, "redirect_uri": callbackURL, "scope": "openid email profile",
		"code_challenge_method": "S256", "prompt": "select_account", "access_type": "online", "hd": googleHD,
	} {
		if q.Get(k) != want {
			t.Errorf("authorization request %s = %q, want %q", k, q.Get(k), want)
		}
	}
	if len(q.Get("code_challenge")) != 43 || len(q.Get("state")) < 40 || len(q.Get("nonce")) < 40 {
		t.Errorf("weak or missing challenge/state/nonce: %v", q)
	}

	// The flow is stored by hash, with the validated rd and the bind hash from the URL.
	hash, _ := session.HashToken(c.Value)
	bind, err := base64.RawURLEncoding.DecodeString(f.loginQ.Get("bind"))
	if err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM oauth_flows WHERE token_hash = $1 AND rd = $2 AND bind_hash = $3 AND state = $4 AND nonce = $5
		AND consumed_at IS NULL AND expires_at > now() + interval '9 minutes' AND expires_at <= now() + interval '10 minutes'`,
		hash, "https://"+hostA+"/reports?id=7", bind, q.Get("state"), q.Get("nonce")); n != 1 {
		t.Fatalf("stored flow does not match the request (%d rows)", n)
	}

	cb := f.callback(nil)
	if cb.status != http.StatusFound {
		t.Fatalf("callback: %d %q", cb.status, cb.body)
	}
	u, _ := url.Parse(cb.location())
	if u.Scheme != "https" || u.Host != hostA || u.Path != "/_platform/callback" || u.Query().Get("code") == "" {
		t.Fatalf("callback redirect = %q", cb.location())
	}
	if cb.header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("callback Referrer-Policy = %q", cb.header.Get("Referrer-Policy"))
	}
	if cleared := cb.cookie(session.CookieLogin); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("login cookie must be deleted, got %+v", cleared)
	}
	sc := cb.cookie(session.CookieSession)
	if sc == nil || !sc.Secure || !sc.HttpOnly || sc.SameSite != http.SameSiteLaxMode || sc.Path != "/" || sc.Domain != "" || sc.MaxAge <= 0 {
		t.Fatalf("session cookie = %+v", sc)
	}
	if !f.flowBurned() {
		t.Fatal("flow not consumed")
	}

	// JIT user bound to the bare google sub (no prefix).
	if got := e.scalar(`SELECT email || '|' || name || '|' || status FROM users WHERE google_sub = $1`, sub); got != "ana@example.test|Ana Test|active" {
		t.Fatalf("user = %q", got)
	}

	final := b.followCallback(cb)
	if final.status != http.StatusFound || final.location() != "https://"+hostA+"/reports?id=7" {
		t.Fatalf("app callback: %d %q", final.status, final.location())
	}
	if ac := final.cookie(session.CookieApp); ac == nil || !ac.Secure || !ac.HttpOnly {
		t.Fatalf("app cookie = %+v", ac)
	}

	// With a grant the assertion validates with the SDK.
	e.grantUser(e.userIDBySub(sub))
	e.app.InvalidateAccess()
	var ok result
	waitFor(t, "grant to take effect", func() bool {
		ok = b.forward(fwd{host: hostA, uri: "/reports", headers: navHeaders})
		return ok.status == http.StatusOK
	})
	ctx, cancel := context.WithTimeout(context.Background(), testTO)
	defer cancel()
	if _, err := e.verifier(appIDA).Verify(ctx, ok.header.Get("Platform-Assertion")); err != nil {
		t.Fatalf("SDK rejects the assertion: %v", err)
	}

	// Audit: provider google, no tokens anywhere.
	rows := e.auditRows()
	sid := e.scalar(`SELECT id::text FROM sessions`)
	want := fmt.Sprintf(`session.login|%s|%s|{"host": "%s", "provider": "google"}`, e.userIDBySub(sub), sid, hostA)
	if len(rows) != 1 || !sameAuditRow(rows[0], want) {
		t.Fatalf("audit rows = %v, want %s", rows, want)
	}
	leakSurface := e.logs.String() + e.dumpTables()
	secrets := fg.issuedSecrets()
	if len(secrets) != 3 { // code, id token, access token
		t.Fatalf("provider issued %d secrets, want 3", len(secrets))
	}
	for _, s := range append(secrets, b.cookie(authHost, session.CookieSession), f.cookie, u.Query().Get("code")) {
		if s == "" || strings.Contains(leakSurface, s) {
			t.Fatalf("a secret reached the logs or the database: %.12q", s)
		}
	}
}

func TestGoogleLoginRefreshesProfile(t *testing.T) {
	fg := newFakeGoogle(t)
	e := newEnv(t, envOptions{google: fg})
	e.googleLogin(e.newBrowser(), hostA, "/")
	id := e.userIDBySub(defaultAccount().Sub)

	renamed := defaultAccount()
	renamed.Email, renamed.Name = "ana.new@example.test", "Ana Renamed"
	fg.setAccount(renamed)
	e.googleLogin(e.newBrowser(), hostA, "/")

	if n := e.count(`SELECT count(*) FROM users`); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}
	if got := e.scalar(`SELECT id::text || '|' || email || '|' || name FROM users`); got != id+"|ana.new@example.test|Ana Renamed" {
		t.Fatalf("user = %q", got)
	}
}

func TestGoogleLoginRejections(t *testing.T) {
	fg := newFakeGoogle(t)
	e := newEnv(t, envOptions{google: fg})
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	acct := func(f func(*account)) func() {
		return func() {
			a := defaultAccount()
			f(&a)
			fg.setAccount(a)
		}
	}
	claim := func(k string, v any) func() {
		return func() { fg.setMutate(func(c jwt.MapClaims) { c[k] = v }) }
	}

	cases := []struct {
		name         string
		prepare      func()           // provider behavior, before the authorization request
		setup        func(f *gflow)   // tampering after the provider answered, before the callback
		mutate       func(url.Values) // the callback query
		wantStatus   int
		wantExchange bool   // the token endpoint must have been called
		wantBurned   bool   // the flow must be consumed
		wantKeep     bool   // the login cookie must be left alone (nothing was burned by this request)
		wantReason   string // logged by the rejected-identity path
		wantLogHD    string
	}{
		{name: "state differs", mutate: func(q url.Values) { q.Set("state", "x"+q.Get("state")) }, wantStatus: http.StatusBadRequest, wantKeep: true},
		{name: "missing state", mutate: func(q url.Values) { q.Del("state") }, wantStatus: http.StatusBadRequest, wantKeep: true},
		{name: "missing code", mutate: func(q url.Values) { q.Del("code") }, wantStatus: http.StatusBadRequest, wantBurned: true},
		{name: "no login cookie", setup: func(f *gflow) { delete(f.b.jar[authHost], session.CookieLogin) }, wantStatus: http.StatusBadRequest, wantKeep: true},
		{name: "garbage login cookie", setup: func(f *gflow) { f.b.jar[authHost][session.CookieLogin] = "not-a-token" }, wantStatus: http.StatusBadRequest, wantKeep: true},
		{
			name: "flow reused",
			setup: func(f *gflow) {
				if r := f.callback(nil); r.status != http.StatusFound {
					f.t.Fatalf("first callback: %d", r.status)
				}
				f.b.jar[authHost][session.CookieLogin] = f.cookie // the attacker replays the cookie
				delete(f.b.jar[authHost], session.CookieSession)
			},
			wantStatus: http.StatusBadRequest, wantBurned: true, wantKeep: true, // the replay dies at the flow, before the provider
		},
		{
			name:       "flow expired",
			setup:      func(f *gflow) { f.e.exec(`UPDATE oauth_flows SET expires_at = now() - interval '1 second'`) },
			wantStatus: http.StatusBadRequest, wantKeep: true,
		},
		{name: "nonce differs", prepare: claim("nonce", "another-nonce"), wantStatus: http.StatusBadRequest, wantExchange: true, wantBurned: true},
		{name: "email_verified false", prepare: acct(func(a *account) { a.EmailVerified = false }), wantStatus: http.StatusForbidden, wantExchange: true, wantBurned: true, wantReason: "email_not_verified", wantLogHD: googleHD},
		{name: "email_verified as string", prepare: claim("email_verified", "true"), wantStatus: http.StatusBadRequest, wantExchange: true, wantBurned: true},
		{name: "no hd", prepare: acct(func(a *account) { a.HD = "" }), wantStatus: http.StatusForbidden, wantExchange: true, wantBurned: true, wantReason: "hd_missing"},
		{name: "hd outside the allowlist", prepare: acct(func(a *account) { a.HD = "evil.test" }), wantStatus: http.StatusForbidden, wantExchange: true, wantBurned: true, wantReason: "hd_not_allowed", wantLogHD: "evil.test"},
		{name: "wrong audience", prepare: claim("aud", "someone-else.apps.googleusercontent.com"), wantStatus: http.StatusBadRequest, wantExchange: true, wantBurned: true},
		{name: "wrong issuer", prepare: claim("iss", "https://evil.example"), wantStatus: http.StatusBadRequest, wantExchange: true, wantBurned: true},
		{name: "expired ID token", prepare: claim("exp", time.Now().Add(-time.Minute).Unix()), wantStatus: http.StatusBadRequest, wantExchange: true, wantBurned: true},
		{name: "signed by another key", prepare: func() { fg.setSigner(otherKey) }, wantStatus: http.StatusBadRequest, wantExchange: true, wantBurned: true},
		{
			name:       "PKCE verifier mismatch",
			setup:      func(f *gflow) { f.e.exec(`UPDATE oauth_flows SET code_verifier = repeat('a', 43)`) },
			wantStatus: http.StatusBadRequest, wantExchange: true, wantBurned: true,
		},
		{
			name: "provider error",
			mutate: func(q url.Values) {
				q.Del("code")
				q.Set("error", "access_denied")
				q.Set("error_description", "<script>alert(1)</script>")
			},
			wantStatus: http.StatusBadRequest, wantBurned: true,
		},
		{
			name: "disabled user",
			setup: func(f *gflow) {
				f.e.exec(`INSERT INTO users (google_sub, email, name, status) VALUES ($1, 'old@example.test', 'Old', 'disabled')`, defaultAccount().Sub)
			},
			wantStatus: http.StatusForbidden, wantExchange: true, wantBurned: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg.setAccount(defaultAccount())
			fg.setMutate(nil)
			fg.setSigner(nil)
			e.exec(`DELETE FROM users`)
			if tc.prepare != nil {
				tc.prepare()
			}
			b := e.newBrowser()
			f := e.beginGoogle(b, hostA, "/")
			f.t = t
			f.provider()
			if tc.setup != nil {
				tc.setup(f)
			}
			before, sessionsBefore := fg.tokenCalls.Load(), e.count(`SELECT count(*) FROM sessions`)
			logStart := len(e.logRecords("google login rejected"))

			res := f.callback(tc.mutate)

			if res.status != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", res.status, tc.wantStatus, res.body)
			}
			if c := res.cookie(session.CookieLogin); tc.wantKeep && c != nil {
				t.Errorf("login cookie touched although nothing was burned: %+v", c)
			} else if !tc.wantKeep && (c == nil || c.MaxAge >= 0) {
				t.Errorf("login cookie not deleted: %+v", c)
			}
			if res.cookie(session.CookieSession) != nil || res.location() != "" || b.cookie(authHost, session.CookieSession) != "" {
				t.Error("a rejected login produced a session or a redirect")
			}
			if n := e.count(`SELECT count(*) FROM sessions`); n != sessionsBefore {
				t.Errorf("sessions %d -> %d", sessionsBefore, n)
			}
			if called := fg.tokenCalls.Load() > before; called != tc.wantExchange {
				t.Errorf("token endpoint called = %v, want %v", called, tc.wantExchange)
			}
			if burned := f.flowBurned(); burned != tc.wantBurned {
				t.Errorf("flow burned = %v, want %v", burned, tc.wantBurned)
			}
			// The page is generic: nothing of the query, the identity or the provider reaches the user.
			for _, leak := range []string{"access_denied", "script", "ana@example.test", "evil.test", "example.test", f.cbURL.Query().Get("code"), f.cbURL.Query().Get("state")} {
				if leak != "" && strings.Contains(res.body, leak) {
					t.Errorf("response reflects %q: %q", leak, res.body)
				}
			}
			recs := e.logRecords("google login rejected")[logStart:]
			if tc.wantReason == "" {
				if len(recs) != 0 {
					t.Errorf("unexpected rejection log: %v", recs)
				}
			} else if len(recs) != 1 || recs[0]["reason"] != tc.wantReason || recs[0]["hd"] != tc.wantLogHD || recs[0]["email"] != "ana@example.test" {
				t.Errorf("rejection log = %v, want reason %q hd %q and the email", recs, tc.wantReason, tc.wantLogHD)
			}
		})
	}

	// None of the attempts leaked a provider secret.
	logs := e.logs.String()
	for _, s := range fg.issuedSecrets() {
		if strings.Contains(logs, s) {
			t.Fatalf("a provider secret reached the logs: %.12q", s)
		}
	}
}

func TestGoogleLoginTakesRDFromTheFlow(t *testing.T) {
	fg := newFakeGoogle(t)
	e := newEnv(t, envOptions{google: fg})
	b := e.newBrowser()
	f := e.beginGoogle(b, hostA, "/safe")
	f.provider()
	cb := f.callback(func(q url.Values) {
		q.Set("rd", "https://"+hostC+"/evil")
		q.Set("bind", f.loginQ.Get("bind"))
		q.Set("host", hostC)
	})
	if cb.status != http.StatusFound {
		t.Fatalf("callback: %d %q", cb.status, cb.body)
	}
	if u, _ := url.Parse(cb.location()); u.Host != hostA {
		t.Fatalf("redirected to %q, want the app of the flow", cb.location())
	}
	if final := b.followCallback(cb); final.location() != "https://"+hostA+"/safe" {
		t.Fatalf("final redirect = %q", final.location())
	}
	if got := e.scalar(`SELECT host || '|' || rd FROM login_codes`); got != hostA+"|https://"+hostA+"/safe" {
		t.Fatalf("login code = %q", got)
	}
}

func TestGoogleLoginReplacesPreviousSession(t *testing.T) {
	fg := newFakeGoogle(t)
	e := newEnv(t, envOptions{google: fg})
	old := e.newBrowser()
	e.googleLogin(old, hostA, "/")
	oldSID := e.scalar(`SELECT id::text FROM sessions`)
	oldToken := old.cookie(authHost, session.CookieSession)
	if e.count(`SELECT count(*) FROM app_sessions WHERE session_id = $1`, oldSID) != 1 {
		t.Fatal("fixture: expected an app session")
	}

	// A flow started elsewhere is completed by the browser that still holds the old central session.
	other := e.newBrowser()
	f := e.beginGoogle(other, hostB, "/")
	f.provider()
	old.jar[authHost][session.CookieLogin] = f.cookie
	f.b = old
	cb := f.callback(nil)
	if cb.status != http.StatusFound {
		t.Fatalf("callback: %d %q", cb.status, cb.body)
	}

	newToken := cb.cookie(session.CookieSession)
	if newToken == nil || newToken.Value == oldToken {
		t.Fatalf("session cookie not rotated: %+v", newToken)
	}
	if e.count(`SELECT count(*) FROM sessions WHERE id = $1`, oldSID) != 0 || e.count(`SELECT count(*) FROM app_sessions WHERE session_id = $1`, oldSID) != 0 {
		t.Fatal("the previous session (or its app sessions) survived")
	}
	if e.count(`SELECT count(*) FROM sessions`) != 1 {
		t.Fatal("expected exactly one session")
	}
	if r := old.forward(fwd{host: hostA, headers: navHeaders}); r.status != http.StatusFound {
		t.Fatalf("old app cookie still works: %d", r.status)
	}
	uid := e.userIDBySub(defaultAccount().Sub)
	rows := e.auditRows()
	wantRevoke := fmt.Sprintf(`session.revoke|%s|%s|{"reason": "replaced_by_new_login"}`, uid, oldSID)
	if len(rows) != 3 || !sameAuditRow(rows[1], wantRevoke) {
		t.Fatalf("audit rows = %v, want a revoke of %s", rows, oldSID)
	}
}

func TestGoogleLoginWithSessionSkipsTheProvider(t *testing.T) {
	fg := newFakeGoogle(t)
	e := newEnv(t, envOptions{google: fg})
	b := e.newBrowser()
	e.googleLogin(b, hostA, "/")
	flows := e.count(`SELECT count(*) FROM oauth_flows`)

	f := e.beginGoogle(b, hostB, "/")
	if u, _ := url.Parse(f.login.location()); f.login.status != http.StatusFound || u.Host != hostB || u.Path != "/_platform/callback" {
		t.Fatalf("SSO redirect = %d %q", f.login.status, f.login.location())
	}
	if f.login.cookie(session.CookieLogin) != nil || e.count(`SELECT count(*) FROM oauth_flows`) != flows {
		t.Fatal("SSO created a login flow")
	}
}

func TestGoogleDisablesFakeLogin(t *testing.T) {
	fg := newFakeGoogle(t)
	e := newEnv(t, envOptions{google: fg}) // the fake login flag is on
	b := e.newBrowser()
	first := b.forward(fwd{host: hostA, headers: navHeaders})
	q := parseLogin(t, first.location())

	r := b.auth(http.MethodPost, "/login", url.Values{"email": {userEmail}, "rd": {q.Get("rd")}, "bind": {q.Get("bind")}}, sameOrigin)
	if r.status != http.StatusMethodNotAllowed || e.count(`SELECT count(*) FROM users`) != 0 || e.count(`SELECT count(*) FROM sessions`) != 0 {
		t.Fatalf("fake POST with Google configured: %d %q", r.status, r.body)
	}
	g := b.auth(http.MethodGet, "/login?"+q.Encode(), nil, nil)
	if g.status != http.StatusFound || strings.Contains(g.body, "<form") || !strings.HasPrefix(g.location(), fg.srv.URL) {
		t.Fatalf("GET /login: %d %q", g.status, g.location())
	}
	warns := e.logRecords("PLATFORM_FAKE_LOGIN ignored: Google login is configured and the fake login is disabled")
	if len(warns) != 1 || warns[0]["level"] != "WARN" {
		t.Fatalf("warnings = %v", warns)
	}
}

func TestGoogleCallbackRateLimited(t *testing.T) {
	fg := newFakeGoogle(t)
	e := newEnv(t, envOptions{google: fg})
	b := e.newBrowser()
	var limited result
	for range 100 {
		r := b.auth(http.MethodGet, "/oauth2/callback?code=x&state=y", nil, map[string]string{"X-Forwarded-For": "203.0.113.9"})
		if r.status == http.StatusTooManyRequests {
			limited = r
			break
		}
	}
	if limited.status != http.StatusTooManyRequests || limited.header.Get("Retry-After") == "" {
		t.Fatalf("no 429 on the callback: %d", limited.status)
	}
}

func TestGoogleCallbackAbsentWithoutGoogle(t *testing.T) {
	e := newEnv(t, envOptions{})
	if r := e.newBrowser().auth(http.MethodGet, "/oauth2/callback?code=x&state=y", nil, nil); r.status != http.StatusNotFound {
		t.Fatalf("status %d, want 404", r.status)
	}
}

func TestGoogleProviderOutage(t *testing.T) {
	fg := newFakeGoogle(t)
	fg.down.Store(true)
	e := newEnv(t, envOptions{google: fg}) // starts with Google down; New and Run must not block

	// An existing session keeps being served: ForwardAuth and /readyz do not depend on Google.
	uid := e.scalar(`INSERT INTO users (google_sub, email, name) VALUES ('outage-sub', 'o@example.test', 'O') RETURNING id::text`)
	e.grantUser(uid)
	ctx, cancel := context.WithTimeout(context.Background(), testTO)
	defer cancel()
	_, sess, err := e.app.Sessions.CreateSession(ctx, session.CreateSessionInput{UserID: uid})
	if err != nil {
		t.Fatal(err)
	}
	appToken, _, err := e.app.Sessions.CreateAppSession(ctx, sess.ID, appIDA, hostA)
	if err != nil {
		t.Fatal(err)
	}
	existing := e.newBrowser()
	existing.jar[hostA] = map[string]string{session.CookieApp: appToken}

	if e.app.Login.Ready() {
		t.Fatal("login ready while the provider is down")
	}
	fresh := e.newBrowser()
	f := e.beginGoogle(fresh, hostA, "/")
	if f.login.status != http.StatusServiceUnavailable || !strings.Contains(f.login.body, "login provider unavailable") || f.login.cookie(session.CookieLogin) != nil {
		t.Fatalf("GET /login while down: %d %q", f.login.status, f.login.body)
	}
	if e.count(`SELECT count(*) FROM oauth_flows`) != 0 {
		t.Fatal("a flow was stored without a provider")
	}
	cb := f.auth("/oauth2/callback?code=x&state=y")
	if cb.status != http.StatusServiceUnavailable || !strings.Contains(cb.body, "login provider unavailable") {
		t.Fatalf("callback while down: %d %q", cb.status, cb.body)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.internal.URL+"/readyz", nil)
	if r := existing.do(req); r.status != http.StatusOK {
		t.Fatalf("/readyz while Google is down: %d", r.status)
	}
	if r := existing.forward(fwd{host: hostA, uri: "/", headers: navHeaders}); r.status != http.StatusOK || r.header.Get("Platform-Assertion") == "" {
		t.Fatalf("existing session while Google is down: %d", r.status)
	}

	// The provider comes back: login works without restarting.
	fg.down.Store(false)
	waitFor(t, "login provider discovery", e.app.Login.Ready)
	if r := e.googleLogin(e.newBrowser(), hostB, "/"); r.status != http.StatusFound {
		t.Fatalf("login after recovery: %d", r.status)
	}
	if len(e.logRecords("login provider discovery failed, will retry")) == 0 {
		t.Error("discovery failures were not logged")
	}
}

// retryAfter is the Retry-After the auth host sends while the provider is unavailable.
const retryAfter = "5"

func TestGoogleCallbackProviderUnavailable(t *testing.T) {
	cases := []struct {
		name           string
		requestTimeout time.Duration
		prepare        func(fg *fakeGoogle)
		setup          func(f *gflow)
		wantStatus     int
		wantRetry      bool
		wantLog        string // "unavailable" or "rejected": which log line the failure must produce
	}{
		{name: "token endpoint answers 500", prepare: func(fg *fakeGoogle) { fg.tokenStatus.Store(http.StatusInternalServerError) }, wantStatus: http.StatusServiceUnavailable, wantRetry: true, wantLog: "unavailable"},
		{name: "token endpoint answers 429", prepare: func(fg *fakeGoogle) { fg.tokenStatus.Store(http.StatusTooManyRequests) }, wantStatus: http.StatusServiceUnavailable, wantRetry: true, wantLog: "unavailable"},
		{name: "token endpoint hangs past the request deadline", requestTimeout: 500 * time.Millisecond, prepare: func(fg *fakeGoogle) { fg.tokenHang.Store(true) }, wantStatus: http.StatusServiceUnavailable, wantRetry: true, wantLog: "unavailable"},
		{name: "signing keys unreachable", prepare: func(fg *fakeGoogle) { fg.jwksDown.Store(true) }, wantStatus: http.StatusServiceUnavailable, wantRetry: true, wantLog: "unavailable"},
		{name: "invalid_grant is a refusal", setup: func(f *gflow) { f.e.exec(`UPDATE oauth_flows SET code_verifier = repeat('a', 43)`) }, wantStatus: http.StatusBadRequest, wantLog: "rejected"},
		{name: "token endpoint answers 400", prepare: func(fg *fakeGoogle) { fg.tokenStatus.Store(http.StatusBadRequest) }, wantStatus: http.StatusBadRequest, wantLog: "rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := newFakeGoogle(t)
			e := newEnv(t, envOptions{google: fg, requestTimeout: tc.requestTimeout})
			f := e.beginGoogle(e.newBrowser(), hostA, "/")
			f.t = t
			f.provider()
			if tc.prepare != nil {
				tc.prepare(fg)
			}
			if tc.setup != nil {
				tc.setup(f)
			}
			start := time.Now()
			res := f.callback(nil)
			if res.status != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", res.status, tc.wantStatus, res.body)
			}
			if got := res.header.Get("Retry-After"); (tc.wantRetry && got != retryAfter) || (!tc.wantRetry && got != "") {
				t.Errorf("Retry-After = %q, want set=%v", got, tc.wantRetry)
			}
			if tc.requestTimeout > 0 && time.Since(start) > tc.requestTimeout+2*time.Second {
				t.Errorf("callback took %s, the %s request deadline did not cut the exchange", time.Since(start), tc.requestTimeout)
			}
			if c := res.cookie(session.CookieLogin); c == nil || c.MaxAge >= 0 || !f.flowBurned() {
				t.Errorf("a failed exchange must burn the flow and delete the cookie: cookie %+v burned %v", c, f.flowBurned())
			}
			if res.cookie(session.CookieSession) != nil || e.count(`SELECT count(*) FROM sessions`) != 0 {
				t.Error("a failed login produced a session")
			}
			unavailable := len(e.logRecords("google login: provider unavailable"))
			rejected := len(e.logRecords("google login failed: rejected"))
			if (tc.wantLog == "unavailable") != (unavailable == 1) || (tc.wantLog == "rejected") != (rejected == 1) {
				t.Errorf("log lines: unavailable=%d rejected=%d, want %s", unavailable, rejected, tc.wantLog)
			}
		})
	}
}

// A redirect from the token endpoint is never followed: the client secret must not reach its destination.
// The answer is 503 (not 400): a 3xx from the token endpoint is the provider misbehaving or being
// misconfigured, not a verdict on the user's code, and retrying the login is the right thing to ask.
func TestGoogleTokenEndpointRedirectIsNotFollowed(t *testing.T) {
	var hits atomic.Int64
	var leaked atomic.Bool
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body)+r.Header.Get("Authorization")+r.URL.RawQuery, googleSecret) ||
			strings.Contains(r.Header.Get("Authorization"), base64.StdEncoding.EncodeToString([]byte(googleClientID+":"+googleSecret))) {
			leaked.Store(true)
		}
	}))
	defer sink.Close()

	fg := newFakeGoogle(t)
	e := newEnv(t, envOptions{google: fg})
	f := e.beginGoogle(e.newBrowser(), hostA, "/")
	f.provider()
	target := sink.URL + "/steal"
	fg.tokenRedirect.Store(&target)

	res := f.callback(nil)
	if res.status != http.StatusServiceUnavailable || res.header.Get("Retry-After") != retryAfter {
		t.Fatalf("callback = %d (Retry-After %q), want 503", res.status, res.header.Get("Retry-After"))
	}
	if hits.Load() != 0 || leaked.Load() {
		t.Fatalf("the redirect was followed: %d hits, secret leaked %v", hits.Load(), leaked.Load())
	}
	if fg.tokenCalls.Load() == 0 {
		t.Fatal("the token endpoint was never called: the test proves nothing")
	}
}

func TestGoogleLoginSurvivesKeyRotation(t *testing.T) {
	fg := newFakeGoogle(t)
	e := newEnv(t, envOptions{google: fg})
	e.googleLogin(e.newBrowser(), hostA, "/") // loads the first key into go-oidc's cache

	fg.rotateKey() // new kid, the old key is no longer published
	if r := e.googleLogin(e.newBrowser(), hostB, "/"); r.status != http.StatusFound {
		t.Fatalf("login after the provider rotated its keys: %d", r.status)
	}
}

func TestGoogleForeignStateBurnsNothing(t *testing.T) {
	fg := newFakeGoogle(t)
	e := newEnv(t, envOptions{google: fg})

	t.Run("forged callback does not cancel the victim's login", func(t *testing.T) {
		f := e.beginGoogle(e.newBrowser(), hostA, "/")
		f.t = t
		f.provider()
		for name, mutate := range map[string]func(url.Values){
			"foreign state":          func(q url.Values) { q.Set("state", "attacker-state") },
			"foreign state no code":  func(q url.Values) { q.Set("state", "attacker-state"); q.Del("code") },
			"error without state":    func(q url.Values) { q.Del("state"); q.Del("code"); q.Set("error", "access_denied") },
			"error with foreign one": func(q url.Values) { q.Set("state", "attacker-state"); q.Del("code"); q.Set("error", "access_denied") },
		} {
			res := f.callback(mutate)
			if res.status != http.StatusBadRequest || res.cookie(session.CookieLogin) != nil || f.flowBurned() {
				t.Fatalf("%s: status %d, cookie touched %v, burned %v", name, res.status, res.cookie(session.CookieLogin) != nil, f.flowBurned())
			}
		}
		if res := f.callback(nil); res.status != http.StatusFound {
			t.Fatalf("the legitimate callback after the forged ones: %d %q", res.status, res.body)
		}
	})

	t.Run("error with the right state burns the flow", func(t *testing.T) {
		f := e.beginGoogle(e.newBrowser(), hostA, "/")
		f.t = t
		f.provider()
		res := f.callback(func(q url.Values) { q.Del("code"); q.Set("error", "access_denied") })
		if c := res.cookie(session.CookieLogin); res.status != http.StatusBadRequest || c == nil || c.MaxAge >= 0 || !f.flowBurned() {
			t.Fatalf("status %d, cookie %+v, burned %v", res.status, c, f.flowBurned())
		}
	})

	for _, firstOlder := range []bool{true, false} {
		t.Run(fmt.Sprintf("two tabs, older callback first=%v", firstOlder), func(t *testing.T) {
			b := e.newBrowser()
			older := e.beginGoogle(b, hostA, "/older")
			older.t = t
			older.provider()
			newer := e.beginGoogle(b, hostA, "/newer") // replaces the login cookie of the browser
			newer.t = t
			newer.provider()

			var olderRes, newerRes result
			if firstOlder {
				olderRes = older.callback(nil)
				newerRes = newer.callback(nil)
			} else {
				newerRes = newer.callback(nil)
				b.jar[authHost][session.CookieLogin] = newer.cookie // the browser still holds the newer cookie (the success cleared it)
				olderRes = older.callback(nil)
			}
			if olderRes.status != http.StatusBadRequest || olderRes.cookie(session.CookieLogin) != nil {
				t.Errorf("older tab: status %d, cookie touched %v", olderRes.status, olderRes.cookie(session.CookieLogin) != nil)
			}
			if newerRes.status != http.StatusFound {
				t.Fatalf("newer tab: %d %q", newerRes.status, newerRes.body)
			}
			if older.flowBurned() {
				t.Error("the older flow was burned by the newer one")
			}
		})
	}
}
