package forwardauth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"platform/api/internal/access"
	"platform/api/internal/apitoken"
	"platform/api/internal/assertion"
	"platform/api/internal/httpx"
	"platform/api/internal/keys"
)

// Token path (contract block 6, "Non-browser clients", "ForwardAuth token path"). On a portal host an
// Authorization header takes this path exclusively; on any other host it is never read.
const (
	headerAuthorization = "Authorization"
	headerWWWAuth       = "WWW-Authenticate"
	bearerScheme        = "Bearer"

	// RFC 6750 challenges. Only the error code varies; the body of a 401 is the same for every cause.
	challengeBearer            = bearerScheme
	challengeInvalidToken      = bearerScheme + ` error="invalid_token"`
	challengeInsufficientScope = bearerScheme + ` error="insufficient_scope"`

	detailBearerRequired = "authentication required"

	// noSubject is the nil UUID, which no user has (ids are generated). The portal fact of a host does not depend on
	// the subject, so it is cached under this one.
	noSubject = "00000000-0000-0000-0000-000000000000"
)

// Reasons of the token path (decision log line and metrics) beyond the access.Reason* of the portal rules. The
// response never tells them apart; the log does.
const (
	ReasonTokenNoBearer      = "token_no_bearer" // other scheme, no credentials, empty or repeated header
	ReasonTokenMalformed     = "token_malformed"
	ReasonTokenUnknown       = "token_unknown"
	ReasonTokenRevoked       = "token_revoked"
	ReasonTokenExpired       = "token_expired"
	ReasonTokenOwnerInactive = "token_owner_inactive"
	ReasonTokenScope         = "token_scope"
)

// Tokens finds an API token by the SHA-256 of its secret, with its owner, reading the database on every call.
type Tokens interface {
	TokenByHash(ctx context.Context, hash []byte) (rec apitoken.Record, found bool, err error)
}

// TokenUseRecorder is told which token just authorized a request (tokenuse.Recorder).
type TokenUseRecorder interface {
	Touch(id string)
}

// portalHost reports whether the app behind host satisfies the portal rule of the Evaluator (Facts.PortalApp:
// the configured app id and initiative platform, never the app id alone). It goes through the decision cache with
// the nil subject, so a request that carries Authorization costs one query per host every CacheTTL.
func (h *Handler) portalHost(ctx context.Context, host string) (bool, error) {
	key := decisionKey{userID: noSubject, host: host}
	cached, ok, gen := h.decisions.get(key)
	// Not reported to the Observer: it would count in the hit ratio of the per-user decision cache.
	if ok {
		return cached.decision.PortalApp, nil
	}
	d, err := h.cfg.Evaluator.Evaluate(ctx, access.Request{
		Subject:  access.Subject{ID: noSubject},
		Resource: access.Resource{Type: access.ResourceTypeApp, ID: host},
		Action:   access.Action{Name: access.ActionAccess},
	})
	if err != nil {
		return false, err
	}
	h.decisions.put(key, cachedDecision{decision: d}, gen)
	return d.PortalApp, nil
}

// bearerToken extracts the credentials of exactly one "Bearer <token>" header (scheme case-insensitive).
func bearerToken(values []string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	scheme, cred, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, bearerScheme) || cred == "" {
		return "", false
	}
	return cred, true
}

// tokenPath decides a request to a portal host that carries Authorization: format, then one live query by hash,
// then the portal rules, then the scope. It never redirects, never sets a cookie and never consults the cookie.
func (h *Handler) tokenPath(w http.ResponseWriter, r *http.Request, appID, host string) outcome {
	ctx := r.Context()
	// Unlike the session path, a missing X-Forwarded-Method does not fall back to r.Method (always GET from
	// Traefik): the scope check would pass a mutation. Empty never matches GET/HEAD, so a read token fails closed.
	method := strings.ToUpper(strings.TrimSpace(r.Header.Get(headerForwardedMethod)))
	secret, ok := bearerToken(r.Header.Values(headerAuthorization))
	if !ok {
		return tokenUnauthorized(w, outcome{appID: appID}, ReasonTokenNoBearer, challengeBearer)
	}
	if !apitoken.WellFormed(secret) {
		return tokenUnauthorized(w, outcome{appID: appID}, ReasonTokenMalformed, challengeInvalidToken)
	}
	rec, found, err := h.cfg.Tokens.TokenByHash(ctx, apitoken.Hash(secret))
	if err != nil {
		return h.unavailable(w, false, ReasonBackend, err)
	}
	if !found {
		return tokenUnauthorized(w, outcome{appID: appID}, ReasonTokenUnknown, challengeInvalidToken)
	}
	// A known token that fails carries its identity in the log: a revoked token still in use is the leak signal.
	base := outcome{appID: appID, userID: rec.OwnerID, tid: rec.ID}
	switch {
	case rec.Revoked:
		return tokenUnauthorized(w, base, ReasonTokenRevoked, challengeInvalidToken)
	case rec.Expired:
		return tokenUnauthorized(w, base, ReasonTokenExpired, challengeInvalidToken)
	case !rec.OwnerActive:
		return tokenUnauthorized(w, base, ReasonTokenOwnerInactive, challengeInvalidToken)
	case !rec.Usable(): // safety net: a flag added to Record and forgotten above still fails closed
		return tokenUnauthorized(w, base, ReasonTokenUnknown, challengeInvalidToken)
	}

	// The token row and the owner's status above are live. The app and initiative rules come from the same decision
	// cache as sessions (CacheTTL, flushed by access_changed): contract block 6 allows it, the token itself never is.
	d, _, err := h.authorize(ctx, rec.OwnerID, host)
	if err != nil {
		return h.unavailable(w, false, ReasonBackend, err)
	}
	if !d.decision.Allow {
		h.respondError(w, false, http.StatusForbidden, "Access denied", "")
		base.decision, base.reason = DecisionDeny, d.decision.Reason
		return base
	}
	// shortcut: no access_denials row for token requests (the table is keyed to sessions); upgrade if denials by token must be queryable.

	// Scope is a ceiling: read allows GET and HEAD only; an unknown scope (never stored, the column is CHECKed) fails closed.
	if rec.Scope != assertion.ScopeAdmin && (rec.Scope != assertion.ScopeRead || method != http.MethodGet && method != http.MethodHead) {
		w.Header().Set(headerWWWAuth, challengeInsufficientScope)
		httpx.WriteProblem(w, httpx.Problem{Status: http.StatusForbidden, Detail: "the token scope does not allow this method"})
		base.decision, base.reason = DecisionDeny, ReasonTokenScope
		return base
	}

	// Roles stay empty: the portal rule grants no roles (Decide returns []).
	issued, err := h.cfg.Issuer.Issue(assertion.Request{
		Audience: d.decision.AppID, Subject: rec.OwnerID, Email: rec.OwnerEmail, Name: rec.OwnerName,
		APITokenID: rec.ID, SubjectType: rec.OwnerKind, Scope: rec.Scope,
	})
	if err != nil {
		reason := ReasonAssertion
		if errors.Is(err, keys.ErrNoActiveKey) {
			reason = ReasonNoSigner
		}
		o := h.unavailable(w, false, reason, err)
		o.appID, o.userID, o.tid = appID, rec.OwnerID, rec.ID
		return o
	}
	w.Header().Set(HeaderAssertion, issued.Token)
	w.WriteHeader(http.StatusOK)
	if h.cfg.Seen != nil {
		h.cfg.Seen.Touch(host)
	}
	if h.cfg.TokenUse != nil {
		h.cfg.TokenUse.Touch(rec.ID) // memory only; the writer flushes last_used_at
	}
	base.decision, base.reason, base.jti = DecisionAllow, access.ReasonGranted, issued.JTI
	return base
}

// tokenUnauthorized answers 401 with the same body for every cause; only the challenge differs.
func tokenUnauthorized(w http.ResponseWriter, o outcome, reason, challenge string) outcome {
	w.Header().Set(headerWWWAuth, challenge)
	httpx.WriteProblem(w, httpx.Problem{Status: http.StatusUnauthorized, Detail: detailBearerRequired})
	o.decision, o.reason = DecisionUnauthenticated, reason
	return o
}
