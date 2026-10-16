package platformauth_test

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformauth "platform/sdk/go"
)

func TestMiddleware(t *testing.T) {
	signer := newKey(t)

	type seen struct {
		called bool
		claims *platformauth.Claims
		header string
	}
	run := func(t *testing.T, method, path string, mutate func(*http.Request, string)) (*httptest.ResponseRecorder, *seen, string) {
		t.Helper()
		srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: signer})
		clock := newClock()
		v := newVerifier(t, srv, clock)
		var logs bytes.Buffer
		log := slog.New(slog.NewTextHandler(&logs, nil))

		got := &seen{}
		h := v.MiddlewareWithLogger(log, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got.called = true
			got.claims, _ = platformauth.ClaimsFrom(r.Context())
			got.header = r.Header.Get(platformauth.HeaderName)
			w.WriteHeader(http.StatusNoContent)
		}))

		token := mint(t, signer, nil, baseClaims(clock.Now()))
		req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
		if mutate != nil {
			mutate(req, token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if strings.Contains(logs.String(), token) || strings.Contains(rec.Body.String(), token) {
			t.Error("token leaked into logs or response")
		}
		return rec, got, token
	}

	assertProblem := func(t *testing.T, rec *httptest.ResponseRecorder, got *seen) {
		t.Helper()
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("Content-Type = %q", ct)
		}
		var body struct {
			Status int `json:"status"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Status != http.StatusUnauthorized {
			t.Errorf("body = %s (err %v)", rec.Body.String(), err)
		}
		if got.called {
			t.Error("handler called on rejection")
		}
	}

	t.Run("valid assertion reaches the handler with typed claims", func(t *testing.T) {
		rec, got, _ := run(t, http.MethodGet, "/dashboard", func(r *http.Request, tok string) {
			r.Header.Set(platformauth.HeaderName, tok)
			r.Header.Set("X-Forwarded-User", "attacker@example.test")
		})
		if rec.Code != http.StatusNoContent || !got.called {
			t.Fatalf("status = %d called = %v", rec.Code, got.called)
		}
		if got.claims == nil || got.claims.Subject != "0198a1e2-0000-7000-8000-000000000001" || got.claims.Audience != testAudience {
			t.Errorf("claims = %+v", got.claims)
		}
		if got.header != "" {
			t.Error("assertion header still visible to the handler")
		}
	})
	t.Run("missing header", func(t *testing.T) {
		rec, got, _ := run(t, http.MethodGet, "/dashboard", nil)
		assertProblem(t, rec, got)
	})
	t.Run("invalid token", func(t *testing.T) {
		rec, got, _ := run(t, http.MethodGet, "/dashboard", func(r *http.Request, _ string) {
			r.Header.Set(platformauth.HeaderName, "not.a.jwt")
		})
		assertProblem(t, rec, got)
	})
	t.Run("repeated header is ambiguous", func(t *testing.T) {
		rec, got, _ := run(t, http.MethodGet, "/dashboard", func(r *http.Request, tok string) {
			r.Header.Add(platformauth.HeaderName, tok)
			r.Header.Add(platformauth.HeaderName, tok)
		})
		assertProblem(t, rec, got)
	})
	t.Run("health path is exempt", func(t *testing.T) {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec, got, _ := run(t, method, "/healthz", nil)
			if rec.Code != http.StatusNoContent || !got.called {
				t.Errorf("%s /healthz: status = %d called = %v", method, rec.Code, got.called)
			}
		}
	})
	t.Run("exemption is exact", func(t *testing.T) {
		rec, got, _ := run(t, http.MethodGet, "/healthz/extra", nil)
		assertProblem(t, rec, got)
	})
}
