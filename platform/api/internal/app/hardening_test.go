package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"platform/api/internal/app"
	"platform/api/internal/keys"
	"platform/api/internal/postgres"
	"platform/api/internal/session"
)

const sqlstateInsufficientPrivilege = "42501"

// auditRows returns "action|actor|target|after" for every audit event, oldest first.
func (e *env) auditRows() []string {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTO)
	defer cancel()
	rows, err := e.pool.Query(ctx, `SELECT action || '|' || coalesce(actor_id::text, '') || '|' || target_id || '|' || coalesce(after::text, '')
		FROM audit_events ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestAuditEvents(t *testing.T) {
	e := newEnv(t, envOptions{})

	// Login (fake) and logout from the auth host.
	a := e.newBrowser()
	a.login(hostA, "/", "a@example.test")
	sidA := e.scalar(`SELECT id::text FROM sessions WHERE user_id = $1`, e.userID("a@example.test"))
	tokenA, appTokenA := a.cookie(authHost, session.CookieSession), a.cookie(hostA, session.CookieApp)
	if r := a.auth(http.MethodPost, "/logout", nil, sameOrigin); r.status != http.StatusSeeOther {
		t.Fatalf("auth host logout: %d", r.status)
	}

	// Logout from an app host.
	b := e.newBrowser()
	b.login(hostB, "/", "b@example.test")
	sidB := e.scalar(`SELECT id::text FROM sessions WHERE user_id = $1`, e.userID("b@example.test"))
	if r := b.forward(fwd{host: hostB, method: http.MethodPost, uri: "/_platform/logout", headers: map[string]string{"Sec-Fetch-Site": "same-origin"}}); r.status != http.StatusSeeOther {
		t.Fatalf("app logout: %d", r.status)
	}

	// A new login on top of an existing session revokes the old one.
	c := e.newBrowser()
	c.login(hostA, "/", "c@example.test")
	sidC := e.scalar(`SELECT id::text FROM sessions WHERE user_id = $1`, e.userID("c@example.test"))
	q := parseLogin(t, c.forward(fwd{host: hostB, headers: navHeaders}).location())
	if r := c.auth(http.MethodPost, "/login", url.Values{"email": {"d@example.test"}, "rd": {q.Get("rd")}, "bind": {q.Get("bind")}}, sameOrigin); r.status != http.StatusFound {
		t.Fatalf("relogin: %d", r.status)
	}
	sidD := e.scalar(`SELECT id::text FROM sessions WHERE user_id = $1`, e.userID("d@example.test"))

	uid := e.userID
	want := []string{
		fmt.Sprintf(`session.login|%s|%s|{"host": "%s", "method": "fake"}`, uid("a@example.test"), sidA, hostA),
		fmt.Sprintf(`session.logout|%s|%s|{"via": "auth_host"}`, uid("a@example.test"), sidA),
		fmt.Sprintf(`session.login|%s|%s|{"host": "%s", "method": "fake"}`, uid("b@example.test"), sidB, hostB),
		fmt.Sprintf(`session.logout|%s|%s|{"via": "app", "host": "%s", "app_id": "%s"}`, uid("b@example.test"), sidB, hostB, appIDB),
		fmt.Sprintf(`session.login|%s|%s|{"host": "%s", "method": "fake"}`, uid("c@example.test"), sidC, hostA),
		fmt.Sprintf(`session.revoke|%s|%s|{"reason": "replaced_by_new_login"}`, uid("c@example.test"), sidC),
		fmt.Sprintf(`session.login|%s|%s|{"host": "%s", "method": "fake"}`, uid("d@example.test"), sidD, hostB),
	}
	got := e.auditRows()
	if len(got) != len(want) {
		t.Fatalf("audit rows = %d, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		// jsonb text key order is by length then name; compare as JSON.
		if !sameAuditRow(got[i], want[i]) {
			t.Errorf("row %d:\n got  %s\n want %s", i, got[i], want[i])
		}
	}

	// No token or cookie value in the audit log or in the log lines.
	dump := strings.Join(got, "\n") + e.scalar(`SELECT coalesce(string_agg(audit_events::text, ' '), '') FROM audit_events`)
	for _, secret := range []string{tokenA, appTokenA, a.cookie(authHost, session.CookieSession)} {
		if secret != "" && strings.Contains(dump, secret) {
			t.Fatal("a cookie value reached audit_events")
		}
	}
	logs := e.logs.String()
	if strings.Contains(logs, tokenA) || strings.Contains(logs, appTokenA) {
		t.Fatal("a cookie value reached the logs")
	}
	var logoutLogged bool
	for _, line := range strings.Split(logs, "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil || rec["msg"] != "logout" {
			continue
		}
		if rec["sid"] == sidA && rec["user_id"] == uid("a@example.test") {
			logoutLogged = true
		}
	}
	if !logoutLogged {
		t.Fatal("auth host logout log line lacks sid/user_id")
	}

	// The runtime role the server runs as cannot rewrite history.
	_, err := e.rt.Exec(context.Background(), `UPDATE audit_events SET action = 'tampered'`)
	if pe, ok := err.(*pgconn.PgError); !ok || pe.Code != sqlstateInsufficientPrivilege {
		t.Fatalf("UPDATE audit_events as runtime: %v", err)
	}
	if got := e.scalar(`SELECT current_user::text`); got == postgres.RuntimeRole {
		t.Fatal("fixtures are expected to run as the owner")
	}
	if got := func() string {
		var u string
		if err := e.rt.QueryRow(context.Background(), `SELECT current_user::text`).Scan(&u); err != nil {
			t.Fatal(err)
		}
		return u
	}(); got != postgres.RuntimeRole {
		t.Fatalf("server pool runs as %q", got)
	}
}

func sameAuditRow(got, want string) bool {
	g, w := strings.SplitN(got, "|", 4), strings.SplitN(want, "|", 4)
	if len(g) != 4 || len(w) != 4 || g[0] != w[0] || g[1] != w[1] || g[2] != w[2] {
		return false
	}
	var gj, wj map[string]string
	return json.Unmarshal([]byte(g[3]), &gj) == nil && json.Unmarshal([]byte(w[3]), &wj) == nil && fmt.Sprint(gj) == fmt.Sprint(wj)
}

// A request URI that ValidateRD would reject must not send the user to a 400: the login falls back to the root.
func TestLoginSurvivesAnUnusableRequestURI(t *testing.T) {
	e := newEnv(t, envOptions{})
	for _, uri := range []string{"/a//b", "/a%2F%2Fb", "/a%5Cb", "/ok//?x=1"} {
		t.Run(uri, func(t *testing.T) {
			b := e.newBrowser()
			first := b.forward(fwd{host: hostA, uri: uri, headers: navHeaders})
			if first.status != http.StatusFound {
				t.Fatalf("status %d", first.status)
			}
			q := parseLogin(t, first.location())
			if q.Get("rd") != "https://"+hostA+"/" {
				t.Fatalf("rd = %q, want the app root", q.Get("rd"))
			}
			if page := b.auth(http.MethodGet, "/login?"+q.Encode(), nil, nil); page.status != http.StatusOK {
				t.Fatalf("login page for the fallback rd: %d", page.status)
			}
		})
	}
	// A clean path keeps its destination.
	first := e.newBrowser().forward(fwd{host: hostA, uri: "/reports/2026?id=7", headers: navHeaders})
	if rd := parseLogin(t, first.location()).Get("rd"); rd != "https://"+hostA+"/reports/2026?id=7" {
		t.Fatalf("rd = %q", rd)
	}
}

// IPv6 clients are limited per /64: rotating the interface identifier does not buy new buckets.
func TestLoginRateLimitGroupsIPv6By64(t *testing.T) {
	e := newEnv(t, envOptions{})
	b := e.newBrowser()
	limited := false
	for i := range 60 {
		ip := fmt.Sprintf("2001:db8:1:2:%x:%x:%x:%x", i+1, i*7+1, i*13+1, i*17+1)
		if r := b.auth(http.MethodGet, "/login", nil, map[string]string{"X-Forwarded-For": ip}); r.status == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("60 requests from rotating addresses of one /64 were never limited")
	}
	// Another /64 has its own bucket.
	if r := b.auth(http.MethodGet, "/login", nil, map[string]string{"X-Forwarded-For": "2001:db8:1:3::1"}); r.status == http.StatusTooManyRequests {
		t.Fatal("a different /64 shares the bucket")
	}
}

// With the database accepting connections and never answering, ForwardAuth and the auth host end in the
// explicit 503 at the request deadline, well before the server WriteTimeout would drop the connection.
func TestHungDatabaseEndsIn503(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() { // accept and stay silent
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	cfg, err := pgxpool.ParseConfig(fmt.Sprintf("host=%s port=%s user=x password=x dbname=x sslmode=disable", host, port))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	kek, _, err := keys.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := keys.NewKeyring(keys.KeyringConfig{Store: postgres.NewKeyStore(pool), KEK: kek})
	if err != nil {
		t.Fatal(err)
	}
	const requestTimeout = 300 * time.Millisecond
	const writeTimeout = 3 * time.Second // the 503 must arrive long before this
	a, err := app.New(app.Config{AuthHost: authHost, Issuer: issuerURL, FakeLogin: true, Pool: pool, Keyring: ring, RequestTimeout: requestTimeout})
	if err != nil {
		t.Fatal(err)
	}
	serve := func(h http.Handler) *httptest.Server {
		s := httptest.NewUnstartedServer(h)
		s.Config.WriteTimeout = writeTimeout
		s.Start()
		t.Cleanup(s.Close)
		return s
	}
	internal, public := serve(a.Internal), serve(a.Public)
	client := &http.Client{Timeout: 2 * writeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for name, mk := range map[string]func() *http.Request{
		"forwardauth navigation": func() *http.Request {
			r, _ := http.NewRequest(http.MethodGet, internal.URL+"/forwardauth", nil)
			r.Header.Set("X-Forwarded-Host", hostA)
			r.Header.Set("Accept", "text/html")
			return r
		},
		"forwardauth api": func() *http.Request {
			r, _ := http.NewRequest(http.MethodGet, internal.URL+"/forwardauth", nil)
			r.Header.Set("X-Forwarded-Host", hostA)
			return r
		},
		"auth host login": func() *http.Request {
			q := url.Values{"rd": {"https://" + hostA + "/"}, "bind": {strings.Repeat("A", 43)}}
			r, _ := http.NewRequest(http.MethodGet, public.URL+"/login?"+q.Encode(), nil)
			return r
		},
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			resp, err := client.Do(mk())
			if err != nil {
				t.Fatalf("no response (connection dropped?): %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("status %d, want 503", resp.StatusCode)
			}
			if d := time.Since(start); d > writeTimeout/2 {
				t.Fatalf("503 took %s, the request deadline is %s", d, requestTimeout)
			}
		})
	}
}
