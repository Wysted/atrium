package app_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"platform/api/internal/apitoken"
	"platform/api/internal/forwardauth"
)

const (
	portalHost  = "portal.apps.example.test"
	portalAppID = "app_0000000000000000000000000P"
)

// tokenEnv is an env whose portal app lives in the platform initiative on portalHost.
func tokenEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, envOptions{portalAppID: portalAppID})
	platform := e.scalar(`INSERT INTO initiatives (key) VALUES ('platform') RETURNING id::text`)
	e.exec(`INSERT INTO apps (id, initiative_id) VALUES ($1, $2)`, portalAppID, platform)
	e.exec(`INSERT INTO deployments (host, app_id) VALUES ($1, $2)`, portalHost, portalAppID)
	return e
}

// addToken stores a token for the owner and returns its secret. createdAgo/expiresIn are SQL intervals.
func (e *env) addToken(owner, scope, createdAgo, expiresIn string) (secret, id string) {
	e.t.Helper()
	secret, err := apitoken.Generate()
	if err != nil {
		e.t.Fatal(err)
	}
	id = e.scalar(`INSERT INTO api_tokens (user_id, name, token_hash, scope, created_at, expires_at)
		VALUES ($1, 'test', $2, $3, now() - $4::interval, now() + $5::interval) RETURNING id::text`,
		owner, apitoken.Hash(secret), scope, createdAgo, expiresIn)
	return secret, id
}

// bearer plays Traefik for a request with an Authorization header.
func (e *env) bearer(host, method string, authz ...string) result {
	e.t.Helper()
	req, _ := http.NewRequestWithContext(e.t.Context(), http.MethodGet, e.internal.URL+"/forwardauth", nil)
	req.Header.Set("X-Forwarded-Host", host)
	req.Header.Set("X-Forwarded-Method", method)
	req.Header.Set("X-Forwarded-Uri", "/api/v1/me")
	req.Header.Set("Accept", "application/json")
	for _, a := range authz {
		req.Header.Add("Authorization", a)
	}
	return e.newBrowser().do(req)
}

func TestTokenPathAgainstTheRuntimeRole(t *testing.T) {
	e := tokenEnv(t)
	human := e.scalar(`INSERT INTO users (google_sub, email, name) VALUES ('g-1', 'ana@example.test', 'Ana') RETURNING id::text`)
	robot := e.scalar(`INSERT INTO users (kind, name, email) VALUES ('robot', 'deploy-bot', '') RETURNING id::text`)
	v := e.verifier(portalAppID)

	t.Run("human token", func(t *testing.T) {
		secret, tid := e.addToken(human, "admin", "0s", "1h")
		r := e.bearer(portalHost, http.MethodPost, "Bearer "+secret)
		if r.status != http.StatusOK {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		c, err := v.Verify(t.Context(), r.header.Get(forwardauth.HeaderAssertion))
		if err != nil {
			t.Fatalf("SDK rejects the token assertion: %v", err)
		}
		if c.Subject != human || c.SubjectType != "human" || c.APITokenID != tid || c.Scope != "admin" ||
			c.SessionID != "" || c.Email != "ana@example.test" || c.Name != "Ana" || len(c.Roles) != 0 {
			t.Fatalf("claims = %+v", c)
		}
	})

	t.Run("robot token", func(t *testing.T) {
		secret, tid := e.addToken(robot, "read", "0s", "1h")
		r := e.bearer(portalHost, http.MethodGet, "Bearer "+secret)
		if r.status != http.StatusOK {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		c, err := v.Verify(t.Context(), r.header.Get(forwardauth.HeaderAssertion))
		if err != nil {
			t.Fatal(err)
		}
		if c.Subject != robot || c.SubjectType != "robot" || c.APITokenID != tid || c.Scope != "read" || c.Email != "" || c.Name != "deploy-bot" {
			t.Fatalf("claims = %+v", c)
		}
		if r := e.bearer(portalHost, http.MethodDelete, "Bearer "+secret); r.status != http.StatusForbidden ||
			r.header.Get("WWW-Authenticate") != `Bearer error="insufficient_scope"` {
			t.Fatalf("read token DELETE: %d %q", r.status, r.header.Get("WWW-Authenticate"))
		}
	})

	invalid := `Bearer error="invalid_token"`
	t.Run("revoked token fails on the next request", func(t *testing.T) {
		secret, tid := e.addToken(human, "admin", "0s", "1h")
		if r := e.bearer(portalHost, http.MethodGet, "Bearer "+secret); r.status != http.StatusOK {
			t.Fatalf("status %d", r.status)
		}
		e.exec(`UPDATE api_tokens SET revoked_at = now() WHERE id = $1`, tid)
		if r := e.bearer(portalHost, http.MethodGet, "Bearer "+secret); r.status != http.StatusUnauthorized || r.header.Get("WWW-Authenticate") != invalid {
			t.Fatalf("revoked: %d %q", r.status, r.header.Get("WWW-Authenticate"))
		}
	})
	t.Run("expired token", func(t *testing.T) {
		secret, _ := e.addToken(human, "admin", "2h", "-1h")
		if r := e.bearer(portalHost, http.MethodGet, "Bearer "+secret); r.status != http.StatusUnauthorized {
			t.Fatalf("expired: %d", r.status)
		}
	})
	t.Run("disabled owner, human and robot, without revoking the token", func(t *testing.T) {
		for _, owner := range []string{human, robot} {
			secret, _ := e.addToken(owner, "admin", "0s", "1h")
			if r := e.bearer(portalHost, http.MethodGet, "Bearer "+secret); r.status != http.StatusOK {
				t.Fatalf("active owner: %d", r.status)
			}
			e.exec(`UPDATE users SET status = 'disabled' WHERE id = $1`, owner)
			r := e.bearer(portalHost, http.MethodGet, "Bearer "+secret)
			if r.status != http.StatusUnauthorized || r.header.Get("WWW-Authenticate") != invalid {
				t.Fatalf("disabled owner: %d %q", r.status, r.header.Get("WWW-Authenticate"))
			}
			e.exec(`UPDATE users SET status = 'active' WHERE id = $1`, owner)
		}
	})
	t.Run("unknown, malformed and wrong scheme", func(t *testing.T) {
		unknown, err := apitoken.Generate()
		if err != nil {
			t.Fatal(err)
		}
		for authz, challenge := range map[string]string{
			"Bearer " + unknown: invalid, "Bearer nope": invalid, "Basic abc": "Bearer", "Bearer": "Bearer",
		} {
			if r := e.bearer(portalHost, http.MethodGet, authz); r.status != http.StatusUnauthorized || r.header.Get("WWW-Authenticate") != challenge {
				t.Errorf("%q: %d %q, want 401 %q", authz, r.status, r.header.Get("WWW-Authenticate"), challenge)
			}
		}
	})
	t.Run("portal initiative inactive", func(t *testing.T) {
		secret, _ := e.addToken(human, "admin", "0s", "1h")
		e.exec(`UPDATE initiatives SET status = 'inactive' WHERE key = 'platform'`)
		e.app.InvalidateAccess() // the NOTIFY listener does this in production
		if r := e.bearer(portalHost, http.MethodGet, "Bearer "+secret); r.status != http.StatusForbidden || r.header.Get("WWW-Authenticate") != "" {
			t.Fatalf("inactive initiative: %d %q", r.status, r.header.Get("WWW-Authenticate"))
		}
	})
	t.Run("other hosts ignore Authorization", func(t *testing.T) {
		secret, _ := e.addToken(human, "admin", "0s", "1h")
		r := e.bearer(hostA, http.MethodGet, "Bearer "+secret)
		if r.status != http.StatusUnauthorized || r.header.Get("WWW-Authenticate") != "" || !strings.Contains(r.body, "login_url") {
			t.Fatalf("other host: %d %q %s", r.status, r.header.Get("WWW-Authenticate"), r.body)
		}
	})
	t.Run("no log carries a secret", func(t *testing.T) {
		if strings.Contains(e.logs.String(), apitoken.Prefix) || strings.Contains(e.logs.String(), "Bearer ") {
			t.Fatalf("log carries Authorization data: %s", e.logs.String())
		}
	})
}

// last_used_at is written by the flusher through the runtime role, never by a request, and never moves backwards.
func TestTokenLastUsedIsWrittenByTheFlusher(t *testing.T) {
	e := tokenEnv(t)
	human := e.scalar(`INSERT INTO users (google_sub, email, name) VALUES ('g-1', 'ana@example.test', 'Ana') RETURNING id::text`)
	secret, tid := e.addToken(human, "read", "0s", "1h")
	used := func() string {
		return e.scalar(`SELECT coalesce(last_used_at::text, '') FROM api_tokens WHERE id = $1`, tid)
	}

	// Failures leave nothing to flush.
	e.bearer(portalHost, http.MethodGet, "Bearer nope")
	if n := e.app.TokenUse.Pending(); n != 0 {
		t.Fatalf("%d tokens pending after a failed request", n)
	}
	for range 3 {
		if r := e.bearer(portalHost, http.MethodGet, "Bearer "+secret); r.status != http.StatusOK {
			t.Fatalf("status %d", r.status)
		}
	}
	if n := e.app.TokenUse.Pending(); n != 1 || used() != "" {
		t.Fatalf("pending %d, last_used_at %q: a request must only queue", n, used())
	}
	if err := e.app.TokenUse.Flush(t.Context()); err != nil {
		t.Fatalf("flush through the runtime role: %v", err)
	}
	first := used()
	if first == "" || e.app.TokenUse.Pending() != 0 {
		t.Fatalf("last_used_at %q, pending %d after the flush", first, e.app.TokenUse.Pending())
	}
	// A revoked token is not written (and the flush does not fail on it).
	e.bearer(portalHost, http.MethodGet, "Bearer "+secret)
	e.exec(`UPDATE api_tokens SET revoked_at = now(), last_used_at = now() + interval '1 hour' WHERE id = $1`, tid)
	if err := e.app.TokenUse.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if e.scalar(`SELECT (last_used_at > now())::text FROM api_tokens WHERE id = $1`, tid) != "true" {
		t.Fatal("the flush moved last_used_at backwards")
	}
}

// A failed token flush shows up in platform_token_use_flush_errors_total (and the batch stays queued).
func TestTokenFlushErrorsAreExposed(t *testing.T) {
	e := tokenEnv(t)
	human := e.scalar(`INSERT INTO users (google_sub, email, name) VALUES ('g-1', 'ana@example.test', 'Ana') RETURNING id::text`)
	secret, _ := e.addToken(human, "read", "0s", "1h")
	e.bearer(portalHost, http.MethodGet, "Bearer "+secret)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := e.app.TokenUse.Flush(ctx); err == nil {
		t.Fatal("flush with a canceled context succeeded")
	}
	if got := e.metric("platform_token_use_flush_errors_total", map[string]string{}); got != 1 {
		t.Fatalf("platform_token_use_flush_errors_total = %v, want 1", got)
	}
	if e.app.TokenUse.Pending() != 1 {
		t.Fatal("the failed batch was lost")
	}
}
