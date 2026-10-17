package portalhttp

import (
	"math"
	"net/http"
	"strings"
	"time"

	"platform/api/internal/httpx"
	"platform/api/internal/portal"
	platformauth "platform/sdk/go"
)

// Routes of the API tokens (contract block 6, "Non-browser clients"). A literal segment ("current") wins over
// the {id} wildcard in the same position.
const (
	PathMeTokens       = "/api/v1/me/tokens"         //nolint:gosec // G101: a URL path, not a credential
	PathMeToken        = "/api/v1/me/tokens/{id}"    //nolint:gosec // G101: a URL path, not a credential
	PathMeTokenCurrent = "/api/v1/me/tokens/current" //nolint:gosec // G101: a URL path, not a credential
	PathToken          = "/api/v1/tokens/{id}"       //nolint:gosec // G101: a URL path, not a credential
	PathTokensRevoke   = "/api/v1/tokens/revoke"     //nolint:gosec // G101: a URL path, not a credential
)

// wwwAuthenticateInsufficientScope is the RFC 6750 challenge of a request whose token scope does not allow it.
const wwwAuthenticateInsufficientScope = `Bearer error="insufficient_scope"`

// TokenScope enforces the scope of an API token from the verified assertion, between authentication and the
// routes (defense in depth: ForwardAuth already checks it). A session assertion has no tid, scope or sub_type and passes. With a
// tid, the scope must be read or admin (anything else is refused, fail closed), and read means exactly GET and
// HEAD: isSafeMethod is not used because it also accepts OPTIONS. A refusal is a 403 with the RFC 6750
// challenge. On success the token id goes to the request context, so audit rows record it.
func TokenScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := platformauth.ClaimsFrom(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if claims.APITokenID == "" {
			// A session assertion carries none of the token claims; scope or sub_type without a tid is
			// malformed, and the session-only rule of token creation relies on this check: fail closed.
			if claims.Scope != "" || claims.SubjectType != "" {
				w.Header().Set("WWW-Authenticate", wwwAuthenticateInsufficientScope)
				httpx.WriteProblem(w, httpx.Problem{Status: http.StatusForbidden})
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if !uuidPattern.MatchString(claims.APITokenID) || !scopeAllows(claims.Scope, r.Method) {
			w.Header().Set("WWW-Authenticate", wwwAuthenticateInsufficientScope)
			httpx.WriteProblem(w, httpx.Problem{Status: http.StatusForbidden})
			return
		}
		next.ServeHTTP(w, r.WithContext(portal.WithToken(r.Context(), strings.ToLower(claims.APITokenID))))
	})
}

func scopeAllows(scope, method string) bool {
	switch scope {
	case portal.TokenScopeRead:
		return method == http.MethodGet || method == http.MethodHead
	case portal.TokenScopeAdmin:
		return true
	default:
		return false
	}
}

func (a *api) tokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+PathMeTokens, a.listOwnTokens)
	mux.HandleFunc("POST "+PathMeTokens, a.createOwnToken)
	mux.HandleFunc("DELETE "+PathMeTokenCurrent, a.revokeCurrentToken)
	mux.HandleFunc("DELETE "+PathMeToken, a.revokeOwnToken)
	mux.HandleFunc("DELETE "+PathToken, a.revokeToken)
	mux.HandleFunc("POST "+PathTokensRevoke, a.revokeBySecret)
}

// createTokenRequest is the body of POST /me/tokens. expires_in is in seconds (optional; the default and the
// maximum are the personal cap of the configuration).
type createTokenRequest struct {
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	ExpiresIn *int64 `json:"expires_in"`
}

// tokenJSON is a token as listed: never the secret, never the hash.
type tokenJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// createdTokenJSON is the creation answer: the only response that carries the secret.
type createdTokenJSON struct {
	tokenJSON
	Secret string `json:"secret"`
}

func tokenToJSON(t portal.APIToken) tokenJSON {
	return tokenJSON{
		ID: t.ID, Name: t.Name, Scope: t.Scope, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
		LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt,
	}
}

// maxExpiresInSeconds is the largest expires_in that fits a time.Duration; larger values are refused by the cap.
const maxExpiresInSeconds = int64(math.MaxInt64 / int64(time.Second))

// newTokenFrom converts the request; expires_in (seconds) is range-checked before the multiplication, so a huge
// negative value cannot wrap into a valid duration.
func newTokenFrom(in createTokenRequest) portal.NewToken {
	req := portal.NewToken{Name: in.Name, Scope: in.Scope}
	if in.ExpiresIn != nil {
		var d time.Duration
		switch secs := *in.ExpiresIn; {
		case secs <= 0: // the use case answers 422 invalid
		case secs > maxExpiresInSeconds:
			d = math.MaxInt64 // the use case answers 422 too_large
		default:
			d = time.Duration(secs) * time.Second
		}
		req.ExpiresIn = &d
	}
	return req
}

func (a *api) createOwnToken(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[createTokenRequest](w, r)
	if !ok {
		return
	}
	req := newTokenFrom(in)
	t, err := a.admin.CreateOwnToken(r.Context(), uid, req)
	if err != nil {
		a.fail(w, r, "token_create", err)
		return
	}
	WriteJSON(w, http.StatusCreated, createdTokenJSON{tokenJSON: tokenToJSON(t.APIToken), Secret: t.Secret})
}

func (a *api) listOwnTokens(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	req, ok := pageRequest(w, r)
	if !ok {
		return
	}
	p, err := a.admin.ListOwnTokens(r.Context(), uid, req)
	if err != nil {
		a.fail(w, r, "tokens_list", err)
		return
	}
	WriteJSON(w, http.StatusOK, listResponse("tokens", mapSlice(p.Items, tokenToJSON), p.NextCursor))
}

func (a *api) revokeOwnToken(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	if err := a.admin.RevokeOwnToken(r.Context(), uid, r.PathValue("id")); err != nil {
		a.fail(w, r, "token_revoke_own", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) revokeCurrentToken(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	if err := a.admin.RevokeCurrentToken(r.Context(), uid); err != nil {
		a.fail(w, r, "token_revoke_current", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) revokeToken(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	if err := a.admin.RevokeToken(r.Context(), uid, r.PathValue("id")); err != nil {
		a.fail(w, r, "token_revoke", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// revokeSecretRequest is the body of POST /tokens/revoke: a secret taken from a leak report.
type revokeSecretRequest struct {
	Token string `json:"token"`
}

func (a *api) revokeBySecret(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.caller(w, r)
	if !ok {
		return
	}
	in, ok := DecodeJSON[revokeSecretRequest](w, r)
	if !ok {
		return
	}
	if err := a.admin.RevokeBySecret(r.Context(), uid, in.Token); err != nil {
		a.fail(w, r, "token_revoke_secret", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
