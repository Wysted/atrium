package portalhttp

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"platform/api/internal/portal"
)

const (
	tokenID      = "01a14296-1449-7be0-957b-4b1ad8d7a0aa" //nolint:gosec // G101: a test uuid, not a credential
	insufficient = `Bearer error="insufficient_scope"`
)

// tokenCalls is the token part of fakeAdmin: it records what the handlers pass and what the context carries.
type tokenCalls struct {
	newToken portal.NewToken
	tokens   portal.Page[portal.APIToken]
	revoked  string // which use case ran: own, current, any or secret
	secret   string
	ctxToken string // portal.TokenFrom of the last call
}

func (f *fakeAdmin) CreateOwnToken(ctx context.Context, caller string, in portal.NewToken) (portal.CreatedToken, error) {
	f.newToken, f.ctxToken = in, portal.TokenFrom(ctx)
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	return portal.CreatedToken{
		APIToken: portal.APIToken{ID: tokenID, UserID: caller, Name: in.Name, Scope: in.Scope, CreatedAt: exp.Add(-time.Hour), ExpiresAt: exp},
		Secret:   "ptk_secret",
	}, f.note(caller, "")
}

func (f *fakeAdmin) ListOwnTokens(ctx context.Context, caller string, req portal.PageRequest) (portal.Page[portal.APIToken], error) {
	f.req, f.ctxToken = req, portal.TokenFrom(ctx)
	return f.tokens, f.note(caller, "")
}

func (f *fakeAdmin) RevokeOwnToken(ctx context.Context, caller, id string) error {
	f.revoked, f.ctxToken = "own", portal.TokenFrom(ctx)
	return f.note(caller, id)
}

func (f *fakeAdmin) RevokeCurrentToken(ctx context.Context, caller string) error {
	f.revoked, f.ctxToken = "current", portal.TokenFrom(ctx)
	return f.note(caller, "")
}

func (f *fakeAdmin) RevokeToken(ctx context.Context, caller, id string) error {
	f.revoked, f.ctxToken = "any", portal.TokenFrom(ctx)
	return f.note(caller, id)
}

func (f *fakeAdmin) RevokeBySecret(ctx context.Context, caller, secret string) error {
	f.revoked, f.secret, f.ctxToken = "secret", secret, portal.TokenFrom(ctx)
	return f.note(caller, "")
}

func TestTokenScopeMiddleware(t *testing.T) {
	t.Parallel()
	const (
		pathGroups = "/api/v1/groups"
		upperTID   = "01A14296-1449-7BE0-957B-4B1AD8D7A0AA"
	)
	allMethods := []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	tests := []struct {
		name    string
		headers map[string]string
		allowed []string // methods that reach the routes; every other one is a 403 with the challenge
	}{
		{"browser session: no tid", nil, allMethods},
		{"read", map[string]string{"X-Test-Tid": tokenID, "X-Test-Scope": "read"}, []string{http.MethodGet, http.MethodHead}},
		{"admin", map[string]string{"X-Test-Tid": tokenID, "X-Test-Scope": "admin"}, allMethods},
		{"scope without tid fails closed", map[string]string{"X-Test-Scope": "admin"}, nil},
		{"sub_type without tid fails closed", map[string]string{"X-Test-SubType": "robot"}, nil},
		{"scope and sub_type without tid fail closed", map[string]string{"X-Test-Scope": "read", "X-Test-SubType": "human"}, nil},
		{"empty scope fails closed", map[string]string{"X-Test-Tid": tokenID}, nil},
		{"unknown scope fails closed", map[string]string{"X-Test-Tid": tokenID, "X-Test-Scope": "write"}, nil},
		{"scope is case sensitive", map[string]string{"X-Test-Tid": tokenID, "X-Test-Scope": "Admin"}, nil},
		{"tid that is not a uuid fails closed", map[string]string{"X-Test-Tid": "not-a-uuid", "X-Test-Scope": "admin"}, nil},
		{"uppercase tid is accepted", map[string]string{"X-Test-Tid": upperTID, "X-Test-Scope": "read"}, []string{http.MethodGet, http.MethodHead}},
	}
	for _, tt := range tests {
		for _, method := range allMethods {
			t.Run(tt.name+"/"+method, func(t *testing.T) {
				t.Parallel()
				fake := &fakeAdmin{}
				w := do(newTestHandler(t, fakeService{}, fake, nil), method, pathGroups, "{}", tt.headers)
				allowed := false
				for _, m := range tt.allowed {
					allowed = allowed || m == method
				}
				if allowed {
					if w.Header().Get("WWW-Authenticate") != "" {
						t.Fatalf("status %d: the scope refused an allowed method", w.Code)
					}
					return
				}
				if w.Code != http.StatusForbidden || w.Header().Get("WWW-Authenticate") != insufficient {
					t.Fatalf("status %d, WWW-Authenticate %q; want 403 with the challenge", w.Code, w.Header().Get("WWW-Authenticate"))
				}
				if fake.calls != 0 {
					t.Fatal("a refused request reached the use case")
				}
			})
		}
	}
}

// Self-revocation needs the admin scope like any other DELETE: no path is exempt from read.
func TestReadTokenCannotRevokeItself(t *testing.T) {
	t.Parallel()
	fake := &fakeAdmin{}
	h := newTestHandler(t, fakeService{}, fake, nil)
	read := map[string]string{"X-Test-Tid": tokenID, "X-Test-Scope": "read"}
	w := do(h, http.MethodDelete, PathMeTokenCurrent, "{}", read)
	if w.Code != http.StatusForbidden || w.Header().Get("WWW-Authenticate") != insufficient || fake.calls != 0 {
		t.Fatalf("read token: status %d, challenge %q, calls %d", w.Code, w.Header().Get("WWW-Authenticate"), fake.calls)
	}
	read["X-Test-Scope"] = "admin"
	if w := do(h, http.MethodDelete, PathMeTokenCurrent, "{}", read); w.Code != http.StatusNoContent || fake.revoked != "current" {
		t.Fatalf("admin token: status %d, revoked via %q", w.Code, fake.revoked)
	}
}

func TestTokenScopePutsTheTokenInTheContext(t *testing.T) {
	t.Parallel()
	fake := &fakeAdmin{}
	h := newTestHandler(t, fakeService{}, fake, nil)
	do(h, http.MethodGet, PathMeTokens, "", map[string]string{"X-Test-Tid": "01A14296-1449-7BE0-957B-4B1AD8D7A0AA", "X-Test-Scope": "read"})
	if fake.ctxToken != tokenID { // lowercased, like the user id
		t.Fatalf("context token = %q, want %q", fake.ctxToken, tokenID)
	}
	do(h, http.MethodGet, PathMeTokens, "", nil)
	if fake.ctxToken != "" {
		t.Fatalf("a session request carried token %q", fake.ctxToken)
	}
}

func TestTokenRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		status  int
		revoked string
	}{
		{"revoke own", http.MethodDelete, "/api/v1/me/tokens/" + tokenID, "{}", http.StatusNoContent, "own"},
		{"revoke current wins over the id pattern", http.MethodDelete, PathMeTokenCurrent, "{}", http.StatusNoContent, "current"},
		{"revoke any", http.MethodDelete, "/api/v1/tokens/" + tokenID, "{}", http.StatusNoContent, "any"},
		{"revoke by secret wins over the id pattern", http.MethodPost, PathTokensRevoke, `{"token":"ptk_x"}`, http.StatusNoContent, "secret"},
		{"no PATCH", http.MethodPatch, "/api/v1/me/tokens/" + tokenID, "{}", http.StatusNotFound, ""},
		{"no GET of one token", http.MethodGet, "/api/v1/me/tokens/" + tokenID, "", http.StatusNotFound, ""},
		{"no DELETE of the collection", http.MethodDelete, PathMeTokens, "{}", http.StatusNotFound, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeAdmin{}
			w := do(newTestHandler(t, fakeService{}, fake, nil), tt.method, tt.path, tt.body, nil)
			if w.Code != tt.status || fake.revoked != tt.revoked {
				t.Fatalf("status %d (want %d), revoked via %q (want %q)", w.Code, tt.status, fake.revoked, tt.revoked)
			}
			if tt.status == http.StatusNoContent && w.Body.Len() != 0 {
				t.Fatalf("204 with body %q", w.Body.String())
			}
		})
	}
}

func TestCreateTokenResponseAndRequest(t *testing.T) {
	t.Parallel()
	fake := &fakeAdmin{}
	h := newTestHandler(t, fakeService{}, fake, nil)

	w := do(h, http.MethodPost, PathMeTokens, `{"name":"laptop","scope":"read","expires_in":7200}`, nil)
	const want = `{"id":"` + tokenID + `","name":"laptop","scope":"read","created_at":"2030-01-02T02:04:05Z",` +
		`"expires_at":"2030-01-02T03:04:05Z","last_used_at":null,"revoked_at":null,"secret":"ptk_secret"}`
	if w.Code != http.StatusCreated || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q: the secret must never be cached", w.Header().Get("Cache-Control"))
	}
	if d := fake.newToken.ExpiresIn; fake.newToken.Name != "laptop" || fake.newToken.Scope != "read" || d == nil || *d != 2*time.Hour {
		t.Fatalf("request = %+v", fake.newToken)
	}

	// Without expires_in the use case picks the cap; a huge value does not overflow into a valid duration.
	do(h, http.MethodPost, PathMeTokens, `{"name":"n","scope":"read"}`, nil)
	if fake.newToken.ExpiresIn != nil {
		t.Fatalf("expires_in = %v, want nil", *fake.newToken.ExpiresIn)
	}
	do(h, http.MethodPost, PathMeTokens, `{"name":"n","scope":"read","expires_in":9223372036854775807}`, nil)
	if d := fake.newToken.ExpiresIn; d == nil || *d <= 0 {
		t.Fatalf("a huge expires_in became %v", d)
	}
	// Negative values must never wrap into a valid lifetime (-36028797018960368 s * 1e9 overflows int64 to a small positive).
	for _, secs := range []string{"-36028797018960368", "-9223372036854775808", "-1", "0"} {
		fake.newToken.ExpiresIn = nil
		do(h, http.MethodPost, PathMeTokens, `{"name":"n","scope":"read","expires_in":`+secs+`}`, nil)
		if d := fake.newToken.ExpiresIn; d == nil || *d > 0 {
			t.Errorf("expires_in %s reached the use case as %v, want a non-positive duration", secs, d)
		}
	}

	for name, body := range map[string]string{
		"unknown field":      `{"name":"n","scope":"read","user_id":"x"}`,
		"expires_in string":  `{"name":"n","scope":"read","expires_in":"1h"}`,
		"expires_in decimal": `{"name":"n","scope":"read","expires_in":1.5}`,
		"not an object":      `[]`,
	} {
		before := fake.calls
		if w := do(h, http.MethodPost, PathMeTokens, body, nil); w.Code != http.StatusBadRequest || fake.calls != before {
			t.Errorf("%s: status %d, reached the use case: %v", name, w.Code, fake.calls != before)
		}
	}
}

func TestListTokensNeverShowsSecretOrHash(t *testing.T) {
	t.Parallel()
	used := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := &fakeAdmin{tokens: portal.Page[portal.APIToken]{
		Items:      []portal.APIToken{{ID: tokenID, UserID: callerID, Name: "n", Scope: "admin", LastUsedAt: &used, RevokedAt: &used}},
		NextCursor: "next",
	}}
	w := do(newTestHandler(t, fakeService{}, fake, nil), http.MethodGet, PathMeTokens+"?limit=5&cursor=abc", "", nil)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `"last_used_at":"2030-01-01T00:00:00Z"`) ||
		!strings.Contains(body, `"revoked_at":"2030-01-01T00:00:00Z"`) || !strings.Contains(body, `"next_cursor":"next"`) {
		t.Fatalf("status %d body %s", w.Code, body)
	}
	for _, leak := range []string{"secret", "hash", "user_id", "created_by"} {
		if strings.Contains(body, leak) {
			t.Errorf("listing contains %q: %s", leak, body)
		}
	}
	if fake.req.Limit != 5 || fake.req.Cursor != "abc" {
		t.Fatalf("page request = %+v", fake.req)
	}
	if w := do(newTestHandler(t, fakeService{}, &fakeAdmin{}, nil), http.MethodGet, PathMeTokens, "", nil); strings.TrimSpace(w.Body.String()) != `{"tokens":[]}` {
		t.Fatalf("empty list = %s", w.Body.String())
	}
}

func TestTokenErrorMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{"limit", portal.ErrTokenLimit, http.StatusConflict},
		{"forbidden (token creating a token)", portal.ErrForbidden, http.StatusForbidden},
		{"not found", portal.ErrNotFound, http.StatusNotFound},
		{"inactive", portal.ErrUserInactive, http.StatusForbidden},
		{"contention", portal.ErrContention, http.StatusServiceUnavailable},
		{"validation", &portal.ValidationError{Problems: []portal.FieldProblem{{Field: "scope", Code: portal.CodeNotAllowed}}}, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newTestHandler(t, fakeService{}, &fakeAdmin{err: tt.err}, nil)
			for _, req := range []struct{ method, path, body string }{
				{http.MethodPost, PathMeTokens, `{"name":"n","scope":"read"}`},
				{http.MethodDelete, "/api/v1/me/tokens/" + tokenID, "{}"},
				{http.MethodDelete, PathMeTokenCurrent, "{}"},
				{http.MethodDelete, "/api/v1/tokens/" + tokenID, "{}"},
			} {
				w := do(h, req.method, req.path, req.body, nil)
				if w.Code != tt.status {
					t.Errorf("%s %s: status %d, want %d", req.method, req.path, w.Code, tt.status)
				}
				if tt.status == http.StatusServiceUnavailable && w.Header().Get("Retry-After") == "" {
					t.Errorf("%s: 503 without Retry-After", req.path)
				}
			}
		})
	}
}
