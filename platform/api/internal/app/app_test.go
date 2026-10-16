package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"platform/api/internal/keys"
	"platform/api/internal/session"
)

const userEmail = "ana@example.test"

func parseLogin(t *testing.T, loc string) url.Values {
	t.Helper()
	u, err := url.Parse(loc)
	if err != nil || u.Scheme != "https" || u.Host != authHost || u.Path != "/login" {
		t.Fatalf("not a login URL: %q", loc)
	}
	return u.Query()
}

func TestDecisionTable(t *testing.T) {
	e := newEnv(t, envOptions{})
	// A user with a session on host A but no grant yet, and a second one with the grant.
	denied := e.newBrowser()
	if r := denied.login(hostA, "/", "nogrant@example.test"); r.status != http.StatusFound {
		t.Fatalf("callback = %d", r.status)
	}
	allowed := e.newBrowser()
	allowed.login(hostA, "/", userEmail) // callback done; first request is a deny (no grant yet)
	e.grant(userEmail)
	e.app.InvalidateAccess()

	t.Run("1 auth host passes without assertion", func(t *testing.T) {
		r := e.newBrowser().forward(fwd{host: authHost, uri: "/login"})
		if r.status != http.StatusOK || r.header.Get("Platform-Assertion") != "" {
			t.Fatalf("status %d, assertion %q", r.status, r.header.Get("Platform-Assertion"))
		}
	})

	t.Run("2 host without deployment is 403", func(t *testing.T) {
		b := e.newBrowser()
		nav := b.forward(fwd{host: "unknown.apps.example.test", headers: navHeaders})
		api := b.forward(fwd{host: "unknown.apps.example.test", headers: apiHeaders})
		if nav.status != http.StatusForbidden || api.status != http.StatusForbidden {
			t.Fatalf("nav %d api %d", nav.status, api.status)
		}
		if !strings.HasPrefix(nav.header.Get("Content-Type"), "text/html") || api.header.Get("Content-Type") != "application/problem+json" {
			t.Fatalf("content types: %q / %q", nav.header.Get("Content-Type"), api.header.Get("Content-Type"))
		}
	})

	t.Run("3 CORS preflight passes without assertion", func(t *testing.T) {
		r := e.newBrowser().forward(fwd{host: hostA, method: http.MethodOptions, headers: map[string]string{
			"Origin": "https://spa.example.test", "Access-Control-Request-Method": "POST"}})
		if r.status != http.StatusOK {
			t.Fatalf("status %d", r.status)
		}
		// OPTIONS without the preflight headers is an ordinary request.
		plain := e.newBrowser().forward(fwd{host: hostA, method: http.MethodOptions, headers: apiHeaders})
		if plain.status != http.StatusUnauthorized {
			t.Fatalf("plain OPTIONS status %d, want 401", plain.status)
		}
	})

	t.Run("6 navigation without cookie redirects to login with bind", func(t *testing.T) {
		for name, tc := range map[string]fwd{
			"accept html":    {host: hostA, uri: "/dash?x=1", headers: map[string]string{"Accept": "text/html"}},
			"fetch navigate": {host: hostA, uri: "/dash?x=1", headers: map[string]string{"Sec-Fetch-Mode": "navigate"}},
			"head":           {host: hostA, uri: "/dash?x=1", method: http.MethodHead, headers: navHeaders},
			"garbage cookie": {host: hostA, uri: "/dash?x=1", headers: navHeaders},
		} {
			t.Run(name, func(t *testing.T) {
				b := e.newBrowser()
				if name == "garbage cookie" {
					b.jar[hostA] = map[string]string{session.CookieApp: "not-a-token"}
				}
				r := b.forward(fwd{host: tc.host, uri: tc.uri, method: tc.method, headers: tc.headers})
				if r.status != http.StatusFound {
					t.Fatalf("status %d", r.status)
				}
				q := parseLogin(t, r.location())
				if q.Get("rd") != "https://"+hostA+"/dash?x=1" {
					t.Errorf("rd = %q", q.Get("rd"))
				}
				c := r.cookie(session.CookieBind)
				if c == nil || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" || c.MaxAge <= 0 {
					t.Fatalf("bind cookie = %+v", c)
				}
				if q.Get("bind") != bindHash(t, c.Value) {
					t.Errorf("bind hash does not match the cookie")
				}
			})
		}
	})

	t.Run("7 API without cookie is 401 problem+json with login_url", func(t *testing.T) {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			r := e.newBrowser().forward(fwd{host: hostA, uri: "/api/items", method: method, headers: apiHeaders})
			if r.status != http.StatusUnauthorized || r.header.Get("Content-Type") != "application/problem+json" {
				t.Fatalf("%s: status %d content-type %q", method, r.status, r.header.Get("Content-Type"))
			}
			var p struct {
				Status   int    `json:"status"`
				LoginURL string `json:"login_url"`
			}
			if err := json.Unmarshal([]byte(r.body), &p); err != nil || p.Status != http.StatusUnauthorized {
				t.Fatalf("body %q: %v", r.body, err)
			}
			q := parseLogin(t, p.LoginURL)
			c := r.cookie(session.CookieBind)
			if c == nil || q.Get("bind") != bindHash(t, c.Value) || q.Get("rd") != "https://"+hostA+"/" {
				t.Fatalf("login_url %q does not match the bind cookie", p.LoginURL)
			}
		}
	})

	t.Run("8 authenticated without access is 403 and recorded once", func(t *testing.T) {
		before := e.count(`SELECT count(*) FROM access_denials WHERE reason = 'no_grant' AND user_id = $1`, e.userID("nogrant@example.test"))
		api := denied.forward(fwd{host: hostA, uri: "/api", headers: apiHeaders})
		nav := denied.forward(fwd{host: hostA, uri: "/", headers: navHeaders})
		if api.status != http.StatusForbidden || api.header.Get("Content-Type") != "application/problem+json" {
			t.Fatalf("api: %d %q", api.status, api.header.Get("Content-Type"))
		}
		if nav.status != http.StatusForbidden || !strings.HasPrefix(nav.header.Get("Content-Type"), "text/html") {
			t.Fatalf("nav: %d %q", nav.status, nav.header.Get("Content-Type"))
		}
		after := e.count(`SELECT count(*) FROM access_denials WHERE reason = 'no_grant' AND user_id = $1`, e.userID("nogrant@example.test"))
		if after-before > 1 {
			t.Fatalf("denials recorded: %d, cached decisions must not flood the table", after-before)
		}
		if after == 0 {
			t.Fatal("no denial recorded")
		}
	})

	t.Run("9 authorized gets an assertion the SDK validates", func(t *testing.T) {
		r := allowed.forward(fwd{host: hostA, uri: "/api", headers: apiHeaders})
		if r.status != http.StatusOK {
			t.Fatalf("status %d", r.status)
		}
		ctx, cancel := context.WithTimeout(context.Background(), testTO)
		defer cancel()
		claims, err := e.verifier(appIDA).Verify(ctx, r.header.Get("Platform-Assertion"))
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		sid := e.scalar(`SELECT s.id::text FROM sessions s WHERE s.user_id = $1`, e.userID(userEmail))
		if claims.Subject != e.userID(userEmail) || claims.Audience != appIDA || claims.Email != userEmail ||
			claims.SessionID != sid || len(claims.Roles) != 1 || claims.Roles[0] != "editor" || claims.TokenID == "" {
			t.Fatalf("claims = %+v", claims)
		}
		// The same user has no grant on the other initiative's app.
		if other := allowed.forward(fwd{host: hostC, headers: apiHeaders}); other.status != http.StatusUnauthorized {
			t.Fatalf("other app without session: %d", other.status)
		}
	})

	t.Run("4 invalid callback is 403 and burns the code", func(t *testing.T) {
		b := e.newBrowser()
		r := b.forward(fwd{host: hostA, uri: "/_platform/callback?code=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", headers: navHeaders})
		if r.status != http.StatusForbidden || b.cookie(hostA, session.CookieApp) != "" {
			t.Fatalf("status %d", r.status)
		}
	})
}

func TestFullFlowAndLogs(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()

	// Nothing can be granted before the user exists: log in first (first request is a 403), then grant.
	cb := b.login(hostA, "/reports?id=7", userEmail)
	if cb.status != http.StatusFound || cb.location() != "https://"+hostA+"/reports?id=7" {
		t.Fatalf("callback: %d %q", cb.status, cb.location())
	}
	if cb.header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("callback Referrer-Policy = %q", cb.header.Get("Referrer-Policy"))
	}
	c := cb.cookie(session.CookieApp)
	if c == nil || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
		t.Fatalf("app cookie = %+v", c)
	}
	if cleared := cb.cookie(session.CookieBind); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("bind cookie must be deleted, got %+v", cleared)
	}
	if b.cookie(hostA, session.CookieBind) != "" {
		t.Fatal("bind cookie still in the jar")
	}
	if r := b.forward(fwd{host: hostA, uri: "/reports", headers: navHeaders}); r.status != http.StatusForbidden {
		t.Fatalf("before grant: %d", r.status)
	}
	e.grant(userEmail)
	var final result
	waitFor(t, "grant to take effect", func() bool {
		final = b.forward(fwd{host: hostA, uri: "/reports", headers: navHeaders})
		return final.status == http.StatusOK
	})
	token := final.header.Get("Platform-Assertion")
	ctx, cancel := context.WithTimeout(context.Background(), testTO)
	defer cancel()
	if _, err := e.verifier(appIDA).Verify(ctx, token); err != nil {
		t.Fatalf("SDK rejects the assertion: %v", err)
	}
	if _, err := e.verifier(appIDB).Verify(ctx, token); err == nil {
		t.Fatal("assertion for app A accepted by app B")
	}

	logs := e.logs.String()
	for _, secret := range []string{b.cookie(hostA, session.CookieApp), b.cookie(authHost, session.CookieSession), token} {
		if secret == "" || strings.Contains(logs, secret) {
			t.Fatalf("secret missing from the jar or leaked into logs")
		}
	}
	var found bool
	for line := range strings.SplitSeq(logs, "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil || rec["msg"] != "forwardauth decision" || rec["decision"] != "allow" {
			continue
		}
		found = true
		for _, k := range []string{"reason", "host", "app_id", "sid", "jti"} {
			if v, _ := rec[k].(string); v == "" {
				t.Errorf("decision log line lacks %q: %s", k, line)
			}
		}
	}
	if !found {
		t.Fatal("no allow decision line logged")
	}
}

// decisionLines returns the "forwardauth decision" log records in order.
func decisionLines(t *testing.T, logs string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(logs, "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == "forwardauth decision" {
			out = append(out, rec)
		}
	}
	return out
}

func TestDecisionLogIdentity(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()
	anon := e.newBrowser()

	b.login(hostA, "/", userEmail)
	uid := e.userID(userEmail)
	sid := e.scalar(`SELECT id::text FROM sessions WHERE user_id = $1`, uid)

	// Each step returns the decision line it produced.
	step := func(do func()) map[string]any {
		t.Helper()
		before := len(decisionLines(t, e.logs.String()))
		do()
		lines := decisionLines(t, e.logs.String())
		if len(lines) != before+1 {
			t.Fatalf("expected one new decision line, got %d", len(lines)-before)
		}
		return lines[len(lines)-1]
	}
	deny := step(func() { b.forward(fwd{host: hostA, uri: "/", headers: navHeaders}) })
	e.grant(userEmail)
	e.app.InvalidateAccess()
	allow := step(func() { b.forward(fwd{host: hostA, uri: "/", headers: navHeaders}) })
	anonymous := step(func() { anon.forward(fwd{host: hostA, uri: "/", headers: navHeaders}) })
	logout := step(func() {
		b.forward(fwd{host: hostA, method: http.MethodPost, uri: "/_platform/logout", headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
	})

	for name, tc := range map[string]struct {
		rec                map[string]any
		decision, reason   string
		user, session, jti bool
	}{
		"deny":           {deny, "deny", "no_grant", true, true, false},
		"allow":          {allow, "allow", "granted", true, true, true},
		"login_redirect": {anonymous, "login_redirect", "no_cookie", false, false, false},
		"logout":         {logout, "logout", "logout_ok", true, true, false},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.rec["decision"] != tc.decision || tc.rec["reason"] != tc.reason {
				t.Fatalf("line = %v", tc.rec)
			}
			for key, want := range map[string]struct {
				present bool
				value   string
			}{"user_id": {tc.user, uid}, "sid": {tc.session, sid}} {
				got, _ := tc.rec[key].(string)
				if want.present && got != want.value || !want.present && got != "" {
					t.Errorf("%s = %q, want present=%v (%q)", key, got, want.present, want.value)
				}
			}
			if jti, _ := tc.rec["jti"].(string); (jti != "") != tc.jti {
				t.Errorf("jti = %q, want present=%v", jti, tc.jti)
			}
		})
	}

	t.Run("callback_ok carries user and sid", func(t *testing.T) {
		var cb map[string]any
		for _, rec := range decisionLines(t, e.logs.String()) {
			if rec["decision"] == "callback" {
				cb = rec
			}
		}
		if cb == nil || cb["reason"] != "callback_ok" || cb["user_id"] != uid || cb["sid"] != sid {
			t.Fatalf("callback line = %v", cb)
		}
	})
	t.Run("denial row has the user", func(t *testing.T) {
		if n := e.count(`SELECT count(*) FROM access_denials WHERE reason = 'no_grant' AND user_id = $1 AND sid = $2`, uid, sid); n != 1 {
			t.Fatalf("denial rows with user and sid = %d", n)
		}
	})
}

func TestSessionSwapping(t *testing.T) {
	e := newEnv(t, envOptions{})
	attacker, victim := e.newBrowser(), e.newBrowser()

	// The attacker starts a login on host A and completes it, but does not redeem the code.
	first := attacker.forward(fwd{host: hostA, headers: navHeaders})
	q := parseLogin(t, first.location())
	post := attacker.auth(http.MethodPost, "/login", url.Values{"email": {"mallory@example.test"}, "rd": {q.Get("rd")}, "bind": {q.Get("bind")}}, sameOrigin)
	if post.status != http.StatusFound {
		t.Fatalf("attacker login: %d", post.status)
	}
	callback, _ := url.Parse(post.location())

	// The victim, who has a bind cookie of their own, opens the attacker's callback link.
	victim.forward(fwd{host: hostA, headers: navHeaders})
	r := victim.forward(fwd{host: hostA, uri: callback.RequestURI(), headers: navHeaders})
	if r.status != http.StatusForbidden || victim.cookie(hostA, session.CookieApp) != "" || r.cookie(session.CookieApp) != nil {
		t.Fatalf("victim: status %d, app cookie set: %v", r.status, victim.cookie(hostA, session.CookieApp) != "")
	}
	// The code is burnt: not even the attacker's own browser can redeem it any more.
	r = attacker.forward(fwd{host: hostA, uri: callback.RequestURI(), headers: navHeaders})
	if r.status != http.StatusForbidden || attacker.cookie(hostA, session.CookieApp) != "" {
		t.Fatalf("attacker after burn: status %d", r.status)
	}
}

func TestSSOSecondAppSkipsLogin(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()
	b.login(hostA, "/", userEmail)
	e.grant(userEmail)
	e.app.InvalidateAccess()

	first := b.forward(fwd{host: hostB, uri: "/inbox", headers: navHeaders})
	if first.status != http.StatusFound {
		t.Fatalf("status %d", first.status)
	}
	q := parseLogin(t, first.location())
	direct := b.auth(http.MethodGet, "/login?"+q.Encode(), nil, nil)
	if direct.status != http.StatusFound || !strings.HasPrefix(direct.location(), "https://"+hostB+"/_platform/callback?code=") {
		t.Fatalf("SSO: %d %q", direct.status, direct.location())
	}
	if direct.header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", direct.header.Get("Referrer-Policy"))
	}
	done := b.followCallback(direct)
	if done.status != http.StatusFound || done.location() != "https://"+hostB+"/inbox" {
		t.Fatalf("callback: %d %q", done.status, done.location())
	}
	if r := b.forward(fwd{host: hostB, uri: "/inbox", headers: navHeaders}); r.status != http.StatusOK {
		t.Fatalf("authorized on B: %d", r.status)
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()
	b.login(hostA, "/", userEmail)
	e.grant(userEmail)
	e.app.InvalidateAccess()
	// A second app session of the same central session.
	if r := b.login(hostB, "/", userEmail); r.status != http.StatusFound {
		t.Fatalf("login B: %d", r.status)
	}
	stolen := b.cookie(hostA, session.CookieApp)

	t.Run("GET is 405", func(t *testing.T) {
		if r := b.forward(fwd{host: hostA, uri: "/_platform/logout", headers: navHeaders}); r.status != http.StatusMethodNotAllowed {
			t.Fatalf("status %d", r.status)
		}
	})
	t.Run("POST without Origin or Sec-Fetch-Site is 403", func(t *testing.T) {
		for _, h := range []map[string]string{nil, {"Origin": "https://evil.example"}, {"Sec-Fetch-Site": "cross-site"}} {
			if r := b.forward(fwd{host: hostA, method: http.MethodPost, uri: "/_platform/logout", headers: h}); r.status != http.StatusForbidden {
				t.Fatalf("headers %v: status %d", h, r.status)
			}
		}
		if r := b.forward(fwd{host: hostA, headers: apiHeaders}); r.status != http.StatusOK {
			t.Fatalf("session must survive refused logouts: %d", r.status)
		}
	})
	t.Run("valid POST revokes central and app sessions", func(t *testing.T) {
		r := b.forward(fwd{host: hostA, method: http.MethodPost, uri: "/_platform/logout", headers: map[string]string{"Sec-Fetch-Site": "same-origin"}})
		if r.status != http.StatusSeeOther || r.location() != "https://"+authHost+"/signed-out" {
			t.Fatalf("status %d location %q", r.status, r.location())
		}
		if c := r.cookie(session.CookieApp); c == nil || c.MaxAge >= 0 {
			t.Fatalf("app cookie not cleared: %+v", c)
		}
		if n := e.count(`SELECT count(*) FROM sessions`) + e.count(`SELECT count(*) FROM app_sessions`); n != 0 {
			t.Fatalf("%d session rows left", n)
		}
		// Even replaying the old cookies, the (cached) decision does not matter: session state is checked every time.
		b.jar[hostA] = map[string]string{session.CookieApp: stolen}
		if r := b.forward(fwd{host: hostA, headers: navHeaders}); r.status != http.StatusFound {
			t.Fatalf("replayed cookie: %d", r.status)
		}
		if r := b.forward(fwd{host: hostB, headers: navHeaders}); r.status != http.StatusFound {
			t.Fatalf("other app after logout: %d", r.status)
		}
	})
}

func TestAuthHostPages(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()
	b.login(hostA, "/", userEmail)

	t.Run("signed-out page and security headers", func(t *testing.T) {
		r := b.auth(http.MethodGet, "/signed-out", nil, nil)
		if r.status != http.StatusOK || !strings.Contains(r.body, "Signed out") {
			t.Fatalf("status %d body %q", r.status, r.body)
		}
		for k, want := range map[string]string{
			"Cache-Control": "no-store", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer",
		} {
			if got := r.header.Get(k); got != want {
				t.Errorf("%s = %q, want %q", k, got, want)
			}
		}
		if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("CSP = %q", csp)
		}
	})
	t.Run("GET /logout is 405 and callback is not served", func(t *testing.T) {
		if r := b.auth(http.MethodGet, "/logout", nil, nil); r.status != http.StatusMethodNotAllowed {
			t.Fatalf("GET /logout: %d", r.status)
		}
		if r := b.auth(http.MethodGet, "/oauth2/callback?code=x&state=y", nil, nil); r.status != http.StatusNotFound {
			t.Fatalf("/oauth2/callback: %d", r.status)
		}
	})
	t.Run("POST /logout needs same origin", func(t *testing.T) {
		if r := b.auth(http.MethodPost, "/logout", url.Values{}, nil); r.status != http.StatusForbidden {
			t.Fatalf("no origin: %d", r.status)
		}
		if e.count(`SELECT count(*) FROM sessions`) != 1 {
			t.Fatal("refused logout revoked the session")
		}
		r := b.auth(http.MethodPost, "/logout", url.Values{}, sameOrigin)
		if r.status != http.StatusSeeOther || r.location() != "/signed-out" {
			t.Fatalf("status %d location %q", r.status, r.location())
		}
		if c := r.cookie(session.CookieSession); c == nil || c.MaxAge >= 0 {
			t.Fatalf("session cookie not cleared: %+v", c)
		}
		if e.count(`SELECT count(*) FROM sessions`) != 0 {
			t.Fatal("session survived logout")
		}
	})
	t.Run("JWKS is public on the auth host and on its own listener", func(t *testing.T) {
		if r := b.auth(http.MethodGet, "/.well-known/jwks.json", nil, nil); r.status != http.StatusOK {
			t.Fatalf("public JWKS: %d", r.status)
		}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.jwks.URL+"/.well-known/jwks.json", nil)
		if r := b.do(req); r.status != http.StatusOK || !strings.Contains(r.body, `"keys"`) {
			t.Fatalf("jwks listener JWKS: %d", r.status)
		}
		req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, e.internal.URL+"/.well-known/jwks.json", nil)
		if r := b.do(req); r.status != http.StatusNotFound {
			t.Fatalf("internal JWKS: %d, want 404", r.status)
		}
	})
	t.Run("/forwardauth is not on the public listener", func(t *testing.T) {
		if r := b.auth(http.MethodGet, "/forwardauth", nil, map[string]string{"X-Forwarded-Host": hostA}); r.status != http.StatusNotFound {
			t.Fatalf("status %d", r.status)
		}
	})
}

func TestLoginValidation(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()
	first := b.forward(fwd{host: hostA, headers: navHeaders})
	valid := parseLogin(t, first.location())
	bind := valid.Get("bind")

	for name, tc := range map[string]struct{ rd, bind string }{
		"foreign host":     {"https://evil.example/", bind},
		"unregistered":     {"https://zzz.apps.example.test/", bind},
		"http scheme":      {"http://" + hostA + "/", bind},
		"userinfo":         {"https://" + hostA + "@evil.example/", bind},
		"protocol relativ": {"//evil.example/", bind},
		"double slash":     {"https://" + hostA + "//evil.example", bind},
		"javascript":       {"javascript:alert(1)", bind},
		"empty rd":         {"", bind},
		"empty bind":       {"https://" + hostA + "/", ""},
		"short bind":       {"https://" + hostA + "/", "abc"},
		"padded bind":      {"https://" + hostA + "/", bind + "="},
	} {
		t.Run(name, func(t *testing.T) {
			r := b.auth(http.MethodGet, "/login?"+url.Values{"rd": {tc.rd}, "bind": {tc.bind}}.Encode(), nil, nil)
			if r.status != http.StatusBadRequest {
				t.Fatalf("GET status %d, want 400", r.status)
			}
			p := b.auth(http.MethodPost, "/login", url.Values{"email": {userEmail}, "rd": {tc.rd}, "bind": {tc.bind}}, sameOrigin)
			if p.status != http.StatusBadRequest {
				t.Fatalf("POST status %d, want 400", p.status)
			}
		})
	}
	if e.count(`SELECT count(*) FROM sessions`) != 0 {
		t.Fatal("a rejected login created a session")
	}

	t.Run("POST without same origin is 403", func(t *testing.T) {
		r := b.auth(http.MethodPost, "/login", url.Values{"email": {userEmail}, "rd": {valid.Get("rd")}, "bind": {bind}}, nil)
		if r.status != http.StatusForbidden || e.count(`SELECT count(*) FROM users`) != 0 {
			t.Fatalf("status %d", r.status)
		}
	})
	t.Run("invalid email is 400", func(t *testing.T) {
		for _, email := range []string{"", "nobody", "Ana <ana@example.test>", strings.Repeat("a", 250) + "@example.test"} {
			r := b.auth(http.MethodPost, "/login", url.Values{"email": {email}, "rd": {valid.Get("rd")}, "bind": {bind}}, sameOrigin)
			if r.status != http.StatusBadRequest {
				t.Fatalf("%q: status %d", email, r.status)
			}
		}
	})
}

func TestFakeLoginDisabled(t *testing.T) {
	e := newEnv(t, envOptions{noFakeLogin: true})
	b := e.newBrowser()
	first := b.forward(fwd{host: hostA, headers: navHeaders})
	q := parseLogin(t, first.location())
	if r := b.auth(http.MethodGet, "/login?"+q.Encode(), nil, nil); r.status != http.StatusServiceUnavailable || !strings.Contains(r.body, "login provider not configured") {
		t.Fatalf("GET: %d %q", r.status, r.body)
	}
	r := b.auth(http.MethodPost, "/login", url.Values{"email": {userEmail}, "rd": {q.Get("rd")}, "bind": {q.Get("bind")}}, sameOrigin)
	if r.status != http.StatusServiceUnavailable || e.count(`SELECT count(*) FROM users`) != 0 || e.count(`SELECT count(*) FROM sessions`) != 0 {
		t.Fatalf("POST: %d", r.status)
	}
}

func TestFakeLoginRevokesPreviousSession(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()
	b.login(hostA, "/", "first@example.test")
	oldSession := b.cookie(authHost, session.CookieSession)
	if oldSession == "" {
		t.Fatal("no session cookie")
	}
	// The browser submits the login form again (e.g. a stale tab) with another email while holding a session.
	first := b.forward(fwd{host: hostB, headers: navHeaders})
	q := parseLogin(t, first.location())
	r := b.auth(http.MethodPost, "/login", url.Values{"email": {"second@example.test"}, "rd": {q.Get("rd")}, "bind": {q.Get("bind")}}, sameOrigin)
	if r.status != http.StatusFound {
		t.Fatalf("status %d", r.status)
	}
	if b.cookie(authHost, session.CookieSession) == oldSession {
		t.Fatal("session cookie was not renewed")
	}
	if n := e.count(`SELECT count(*) FROM sessions WHERE user_id = $1`, e.userID("first@example.test")); n != 0 {
		t.Fatalf("previous session still exists (%d)", n)
	}
	if n := e.count(`SELECT count(*) FROM app_sessions`); n != 0 {
		t.Fatalf("previous app sessions still exist (%d)", n)
	}
}

func TestGrantRevocationNotifiesCache(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()
	b.login(hostA, "/", userEmail)
	e.grant(userEmail)
	waitFor(t, "grant", func() bool { return b.forward(fwd{host: hostA, headers: apiHeaders}).status == http.StatusOK })

	start := time.Now()
	e.revokeGrant(userEmail)
	waitFor(t, "revocation to reach the cache through NOTIFY", func() bool {
		return b.forward(fwd{host: hostA, headers: apiHeaders}).status == http.StatusForbidden
	})
	if time.Since(start) > pollWindow {
		t.Fatal("revocation waited for the TTL")
	}

	e.exec(`INSERT INTO initiative_access (initiative_id, role_id, user_id) VALUES ($1, $2, $3)`, e.initiativeID, e.roleID, e.userID(userEmail))
	waitFor(t, "re-grant", func() bool { return b.forward(fwd{host: hostA, headers: apiHeaders}).status == http.StatusOK })

	e.exec(`UPDATE users SET status = 'disabled' WHERE id = $1`, e.userID(userEmail))
	waitFor(t, "disabled user to be denied", func() bool {
		return b.forward(fwd{host: hostA, headers: apiHeaders}).status == http.StatusForbidden
	})
}

func TestDenialRecordFailureDoesNotChangeResponse(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()
	b.login(hostA, "/", userEmail) // first request already ends in the deny path
	e.exec(`ALTER TABLE access_denials RENAME TO access_denials_gone`)

	other := e.newBrowser()
	other.login(hostA, "/", "other@example.test")
	r := other.forward(fwd{host: hostA, headers: apiHeaders})
	if r.status != http.StatusForbidden {
		t.Fatalf("status %d", r.status)
	}
	if !strings.Contains(e.logs.String(), "cannot record access denial") {
		t.Fatal("failed denial record was not logged")
	}
}

func TestFailClosed(t *testing.T) {
	t.Run("database down", func(t *testing.T) {
		e := newEnv(t, envOptions{})
		b := e.newBrowser()
		b.login(hostA, "/", userEmail)
		e.grant(userEmail)
		waitFor(t, "grant", func() bool { return b.forward(fwd{host: hostA, headers: apiHeaders}).status == http.StatusOK })

		e.rt.Close() // the pool the server uses
		for _, h := range []map[string]string{apiHeaders, navHeaders} {
			if r := b.forward(fwd{host: hostA, headers: h}); r.status != http.StatusServiceUnavailable {
				t.Fatalf("authorized request with the database down: %d", r.status)
			}
		}
		// Not even a cached host or decision lets a request through without a validated session.
		if r := e.newBrowser().forward(fwd{host: hostA, headers: apiHeaders}); r.status != http.StatusServiceUnavailable && r.status != http.StatusUnauthorized {
			t.Fatalf("anonymous request: %d", r.status)
		}
		if r := e.newBrowser().forward(fwd{host: "never-seen.apps.example.test", headers: apiHeaders}); r.status != http.StatusServiceUnavailable {
			t.Fatalf("uncached host: %d", r.status)
		}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.internal.URL+"/readyz", nil)
		if r := b.do(req); r.status != http.StatusServiceUnavailable {
			t.Fatalf("/readyz: %d", r.status)
		}
		req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, e.internal.URL+"/healthz", nil)
		if r := b.do(req); r.status != http.StatusOK {
			t.Fatalf("/healthz: %d", r.status)
		}
		// Public side fails closed too.
		if r := b.auth(http.MethodGet, "/login?rd=https://"+hostA+"/&bind=x", nil, nil); r.status == http.StatusOK || r.status == http.StatusFound {
			t.Fatalf("public login with the database down: %d", r.status)
		}
	})

	t.Run("KEK mismatch", func(t *testing.T) {
		other, _, err := keys.GenerateKEK()
		if err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, envOptions{ringKEK: other})
		if !e.ring.KEKMismatch() {
			t.Fatal("setup: expected a KEK mismatch")
		}
		b := e.newBrowser()
		b.login(hostA, "/", userEmail)
		e.grant(userEmail)
		e.app.InvalidateAccess()
		r := b.forward(fwd{host: hostA, headers: apiHeaders})
		if r.status != http.StatusServiceUnavailable {
			t.Fatalf("authorized request with a KEK mismatch: %d", r.status)
		}
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.internal.URL+"/readyz", nil)
		if r := b.do(req); r.status != http.StatusServiceUnavailable {
			t.Fatalf("/readyz: %d", r.status)
		}
	})

	t.Run("no signer loaded", func(t *testing.T) {
		e := newEnv(t, envOptions{noReload: true})
		b := e.newBrowser()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.internal.URL+"/readyz", nil)
		if r := b.do(req); r.status != http.StatusServiceUnavailable {
			t.Fatalf("/readyz: %d", r.status)
		}
	})
}

func TestReadyz(t *testing.T) {
	e := newEnv(t, envOptions{})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.internal.URL+"/readyz", nil)
	if r := e.newBrowser().do(req); r.status != http.StatusOK || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("/readyz: %d", r.status)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()
	var limited result
	for range 100 {
		if r := b.auth(http.MethodGet, "/login", nil, nil); r.status == http.StatusTooManyRequests {
			limited = r
			break
		}
	}
	if limited.status != http.StatusTooManyRequests || limited.header.Get("Retry-After") == "" {
		t.Fatalf("no 429 after 100 requests: %+v", limited.status)
	}
}

func TestDeniedPageSignOut(t *testing.T) {
	e := newEnv(t, envOptions{})
	const nasty = `<b>"x"&'y'@example.test`
	b := e.newBrowser()
	b.login(hostA, "/", "nogrant@example.test") // session exists, no grant
	e.exec(`UPDATE users SET email = $1 WHERE id = $2::uuid`, nasty, e.userID("nogrant@example.test"))
	e.app.InvalidateAccess()

	page := b.forward(fwd{host: hostA, uri: "/?x=<script>", headers: navHeaders})
	if page.status != http.StatusForbidden || !strings.HasPrefix(page.header.Get("Content-Type"), "text/html") {
		t.Fatalf("status %d, type %q", page.status, page.header.Get("Content-Type"))
	}
	for _, want := range []string{
		"<h1>Access denied</h1>",
		"You are signed in as &lt;b&gt;&#34;x&#34;&amp;&#39;y&#39;@example.test, which does not have access to this application.",
		`<form method="post" action="/_platform/logout"><button type="submit">Sign out</button></form>`,
	} {
		if !strings.Contains(page.body, want) {
			t.Errorf("page lacks %q:\n%s", want, page.body)
		}
	}
	if strings.Contains(page.body, "<b>") || strings.Contains(page.body, "<script") || strings.Contains(page.body, "<script>") {
		t.Errorf("unescaped content:\n%s", page.body)
	}
	csp := page.header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "form-action") || page.header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("security headers: CSP %q XFO %q", csp, page.header.Get("X-Frame-Options"))
	}

	// The API variant is unchanged.
	if api := b.forward(fwd{host: hostA, headers: apiHeaders}); api.header.Get("Content-Type") != "application/problem+json" || strings.Contains(api.body, "Sign out") {
		t.Errorf("api 403: %q %q", api.header.Get("Content-Type"), api.body)
	}

	// The button works for a denied session: 303 to the signed-out page and the session is gone.
	out := b.forward(fwd{host: hostA, method: http.MethodPost, uri: "/_platform/logout", headers: map[string]string{"Origin": "https://" + hostA}})
	if out.status != http.StatusSeeOther || out.location() != "https://"+authHost+"/signed-out" {
		t.Fatalf("logout: %d %q", out.status, out.location())
	}
	if n := e.count(`SELECT count(*) FROM sessions`) + e.count(`SELECT count(*) FROM app_sessions`); n != 0 {
		t.Fatalf("%d session rows left", n)
	}
	if r := b.forward(fwd{host: hostA, headers: navHeaders}); r.status != http.StatusFound {
		t.Fatalf("after logout: %d", r.status)
	}

	t.Run("unknown host and no session show no email or button", func(t *testing.T) {
		for name, r := range map[string]result{
			"unknown host": b.forward(fwd{host: "unknown.apps.example.test", headers: navHeaders}),
			"no session":   e.newBrowser().forward(fwd{host: hostA, headers: navHeaders}),
		} {
			if strings.Contains(r.body, "Sign out") || strings.Contains(r.body, "signed in as") || strings.Contains(r.body, "<form") {
				t.Errorf("%s: status %d body %q", name, r.status, r.body)
			}
		}
	})
}
