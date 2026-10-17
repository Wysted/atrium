package forwardauth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wysted/atrium/platform/api/internal/access"
	"github.com/Wysted/atrium/platform/api/internal/apitoken"
	"github.com/Wysted/atrium/platform/api/internal/assertion"
	"github.com/Wysted/atrium/platform/api/internal/session"
)

const (
	tPortalHost = "portal.apps.example.test"
	tOtherHost  = "other.apps.example.test"
	tPortalApp  = "app_0000000000000000000000000P"
	tOwnerID    = "0198a1e2-0000-7000-8000-000000000001"
	tTokenID    = "0198a1e2-0000-7000-8000-0000000000bb" //nolint:gosec // G101: fixture id, not a credential
	tJTI        = "jti-1"
)

type tApps struct{}

func (tApps) AppForHost(_ context.Context, host string) (string, bool, error) {
	switch host {
	case tPortalHost:
		return tPortalApp, true, nil
	case tOtherHost:
		return "app_other", true, nil
	}
	return "", false, nil
}

// tEvaluator knows the portal app; denyReason makes it deny a real user (initiative inactive and the like).
type tEvaluator struct {
	denyReason string
	calls      int
}

func (e *tEvaluator) Evaluate(_ context.Context, req access.Request) (access.Decision, error) {
	e.calls++
	if req.Resource.ID != tPortalHost {
		return access.Decision{AppID: "app_other", Reason: access.ReasonNoGrant}, nil
	}
	d := access.Decision{AppID: tPortalApp, PortalApp: true, Roles: []string{}}
	switch {
	case req.Subject.ID == noSubject:
		d.Reason = access.ReasonUnknownUser
	case e.denyReason != "":
		d.Reason = e.denyReason
	default:
		d.Allow, d.Reason = true, access.ReasonGranted
	}
	return d, nil
}

type tTokens struct {
	mu    sync.Mutex
	rec   map[string]apitoken.Record // by hex-free key: string(hash)
	err   error
	calls int
}

func (t *tTokens) TokenByHash(_ context.Context, hash []byte) (apitoken.Record, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++
	if t.err != nil {
		return apitoken.Record{}, false, t.err
	}
	rec, ok := t.rec[string(hash)]
	return rec, ok, nil
}

type tIssuer struct{ got []assertion.Request }

func (i *tIssuer) Issue(req assertion.Request) (assertion.Issued, error) {
	i.got = append(i.got, req)
	return assertion.Issued{Token: "assertion-token", JTI: tJTI}, nil
}

type tUse struct{ ids []string }

func (u *tUse) Touch(id string) { u.ids = append(u.ids, id) }

type tSeen struct{ hosts []string }

func (s *tSeen) Touch(host string) { s.hosts = append(s.hosts, host) }

// panicSessions fails the test run if the cookie path is reached.
type panicSessions struct{ Sessions }

func (panicSessions) ValidateAppSession(context.Context, string, string) (session.AppSession, error) {
	panic("the cookie must not be consulted on the token path")
}

type tokenRig struct {
	h      *Handler
	eval   *tEvaluator
	tokens *tTokens
	issuer *tIssuer
	use    *tUse
	seen   *tSeen
	logs   *bytes.Buffer
	obs    *decisionObserver
}

func newTokenRig(t *testing.T, rec apitoken.Record, secret string) *tokenRig {
	t.Helper()
	rig := &tokenRig{
		eval: &tEvaluator{}, tokens: &tTokens{rec: map[string]apitoken.Record{string(apitoken.Hash(secret)): rec}},
		issuer: &tIssuer{}, use: &tUse{}, seen: &tSeen{}, logs: &bytes.Buffer{}, obs: &decisionObserver{},
	}
	h, err := New(Config{
		AuthHost: "auth.example.test", Apps: tApps{}, Sessions: panicSessions{}, Evaluator: rig.eval, Profiles: noopProfiles{},
		Denials: noopDenials{}, Issuer: rig.issuer, Audit: noopAudit{}, Tokens: rig.tokens, TokenUse: rig.use, Seen: rig.seen,
		Observer: rig.obs, Logger: slog.New(slog.NewTextHandler(rig.logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	rig.h = h
	return rig
}

func liveRecord(scope string) apitoken.Record {
	return apitoken.Record{
		ID: tTokenID, Scope: scope, OwnerID: tOwnerID, OwnerKind: assertion.SubjectRobot, OwnerName: "deploy-bot", OwnerActive: true,
	}
}

func newSecret(t *testing.T) string {
	t.Helper()
	s, err := apitoken.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type tReq struct {
	host, method string
	authz        []string
	cookie       string
	headers      map[string]string
}

func (rig *tokenRig) serve(t *testing.T, q tReq) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/forwardauth", nil)
	r.Header.Set(headerForwardedHost, q.host)
	if q.method != "" {
		r.Header.Set(headerForwardedMethod, q.method)
	}
	for _, a := range q.authz {
		r.Header.Add(headerAuthorization, a)
	}
	if q.cookie != "" {
		r.Header.Set("Cookie", q.cookie)
	}
	for k, v := range q.headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	rig.h.ServeHTTP(w, r)
	return w
}

func TestTokenPathSuccess(t *testing.T) {
	secret := newSecret(t)
	rig := newTokenRig(t, liveRecord(assertion.ScopeRead), secret)
	w := rig.serve(t, tReq{host: tPortalHost, method: http.MethodHead, authz: []string{"bearer " + secret}})
	if w.Code != http.StatusOK || w.Header().Get(HeaderAssertion) != "assertion-token" {
		t.Fatalf("status %d, assertion %q", w.Code, w.Header().Get(HeaderAssertion))
	}
	want := assertion.Request{
		Audience: tPortalApp, Subject: tOwnerID, Name: "deploy-bot", APITokenID: tTokenID,
		SubjectType: assertion.SubjectRobot, Scope: assertion.ScopeRead,
	}
	if len(rig.issuer.got) != 1 || rig.issuer.got[0].Audience != want.Audience || rig.issuer.got[0].Subject != want.Subject ||
		rig.issuer.got[0].Email != "" || rig.issuer.got[0].Name != want.Name || rig.issuer.got[0].SessionID != "" ||
		rig.issuer.got[0].APITokenID != want.APITokenID || rig.issuer.got[0].SubjectType != want.SubjectType ||
		rig.issuer.got[0].Scope != want.Scope || len(rig.issuer.got[0].Roles) != 0 {
		t.Fatalf("assertion request = %+v, want %+v", rig.issuer.got, want)
	}
	if len(rig.use.ids) != 1 || rig.use.ids[0] != tTokenID || len(rig.seen.hosts) != 1 {
		t.Fatalf("touched tokens %v hosts %v", rig.use.ids, rig.seen.hosts)
	}
	if len(rig.obs.got) != 1 || rig.obs.got[0] != (recordedDecision{DecisionAllow, access.ReasonGranted}) {
		t.Fatalf("decisions = %v", rig.obs.got)
	}
	log := rig.logs.String()
	for _, want := range []string{"decision=allow", "tid=" + tTokenID, "jti=" + tJTI, "host=" + tPortalHost, "app_id=" + tPortalApp} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q: %s", want, log)
		}
	}
}

// Every failure answers the same body; only the challenge differs, and nothing sets a cookie or redirects.
func TestTokenPathFailures(t *testing.T) {
	secret := newSecret(t)
	other := newSecret(t) // well formed, not stored
	// Change the last checksum digit; replacing it with a fixed "0" left the token valid whenever it already ended in "0".
	badSum := secret[:len(secret)-1] + "0"
	if strings.HasSuffix(secret, "0") {
		badSum = secret[:len(secret)-1] + "1"
	}
	mutate := func(f func(*apitoken.Record)) apitoken.Record {
		rec := liveRecord(assertion.ScopeAdmin)
		f(&rec)
		return rec
	}
	tests := []struct {
		name      string
		rec       apitoken.Record
		authz     []string
		method    string
		denial    string
		status    int
		challenge string
		reason    string
		dbLookups int
	}{
		{"other scheme", liveRecord("admin"), []string{"Basic " + secret}, "", "", 401, "Bearer", ReasonTokenNoBearer, 0},
		{"bearer without credentials", liveRecord("admin"), []string{"Bearer"}, "", "", 401, "Bearer", ReasonTokenNoBearer, 0},
		{"bearer with empty credentials", liveRecord("admin"), []string{"Bearer "}, "", "", 401, "Bearer", ReasonTokenNoBearer, 0},
		{"empty header", liveRecord("admin"), []string{""}, "", "", 401, "Bearer", ReasonTokenNoBearer, 0},
		{"raw token without scheme", liveRecord("admin"), []string{secret}, "", "", 401, "Bearer", ReasonTokenNoBearer, 0},
		{"two headers", liveRecord("admin"), []string{"Bearer " + secret, "Bearer " + secret}, "", "", 401, "Bearer", ReasonTokenNoBearer, 0},
		{"two tokens in one header", liveRecord("admin"), []string{"Bearer " + secret + " " + secret}, "", "", 401, `Bearer error="invalid_token"`, ReasonTokenMalformed, 0},
		{"bad checksum", liveRecord("admin"), []string{"Bearer " + badSum}, "", "", 401, `Bearer error="invalid_token"`, ReasonTokenMalformed, 0},
		{"unknown token", liveRecord("admin"), []string{"Bearer " + other}, "", "", 401, `Bearer error="invalid_token"`, ReasonTokenUnknown, 1},
		{"revoked", mutate(func(r *apitoken.Record) { r.Revoked = true }), []string{"Bearer " + secret}, "", "", 401, `Bearer error="invalid_token"`, ReasonTokenRevoked, 1},
		{"expired", mutate(func(r *apitoken.Record) { r.Expired = true }), []string{"Bearer " + secret}, "", "", 401, `Bearer error="invalid_token"`, ReasonTokenExpired, 1},
		{"owner disabled", mutate(func(r *apitoken.Record) { r.OwnerActive = false }), []string{"Bearer " + secret}, "", "", 401, `Bearer error="invalid_token"`, ReasonTokenOwnerInactive, 1},
		{"read token mutating", liveRecord("read"), []string{"Bearer " + secret}, http.MethodDelete, "", 403, `Bearer error="insufficient_scope"`, ReasonTokenScope, 1},
		{"read token options", liveRecord("read"), []string{"Bearer " + secret}, http.MethodOptions, "", 403, `Bearer error="insufficient_scope"`, ReasonTokenScope, 1},
		{"unknown scope fails closed", liveRecord("write"), []string{"Bearer " + secret}, http.MethodGet, "", 403, `Bearer error="insufficient_scope"`, ReasonTokenScope, 1},
		{"initiative inactive", liveRecord("admin"), []string{"Bearer " + secret}, "", access.ReasonInitiativeInactive, 403, "", access.ReasonInitiativeInactive, 1},
	}
	var first401 string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newTokenRig(t, tt.rec, secret)
			rig.eval.denyReason = tt.denial
			// A valid-looking cookie must never rescue a bad token (Sessions panics if it is read).
			w := rig.serve(t, tReq{host: tPortalHost, method: tt.method, authz: tt.authz, cookie: session.CookieApp + "=whatever"})
			if w.Code != tt.status || w.Header().Get("WWW-Authenticate") != tt.challenge {
				t.Fatalf("status %d challenge %q, want %d %q", w.Code, w.Header().Get("WWW-Authenticate"), tt.status, tt.challenge)
			}
			if w.Header().Get("Set-Cookie") != "" || w.Header().Get("Location") != "" || w.Header().Get(HeaderAssertion) != "" {
				t.Fatalf("headers leak a login or assertion: %v", w.Header())
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") || w.Body.Len() > 4096/8 {
				t.Fatalf("content type %q, body %d bytes", ct, w.Body.Len())
			}
			if strings.Contains(w.Body.String(), "login_url") {
				t.Fatalf("body offers a login: %s", w.Body)
			}
			if tt.status == http.StatusUnauthorized {
				if first401 == "" {
					first401 = w.Body.String()
				} else if w.Body.String() != first401 {
					t.Fatalf("401 body differs by cause: %s vs %s", w.Body, first401)
				}
			}
			if len(rig.issuer.got) != 0 || len(rig.use.ids) != 0 {
				t.Fatal("a failed request issued an assertion or recorded a use")
			}
			if rig.tokens.calls != tt.dbLookups {
				t.Fatalf("token lookups = %d, want %d (format checks come before the database)", rig.tokens.calls, tt.dbLookups)
			}
			if len(rig.obs.got) != 1 || rig.obs.got[0].reason != tt.reason {
				t.Fatalf("decisions = %v, want reason %s", rig.obs.got, tt.reason)
			}
			log := rig.logs.String()
			if strings.Contains(log, secret) || strings.Contains(log, "Bearer") {
				t.Fatalf("log carries the Authorization header or token: %s", log)
			}
			for _, a := range tt.authz { // what was actually SENT, not only the stored secret
				for _, f := range strings.Fields(a) {
					if len(f) > len(bearerScheme) && strings.Contains(log, f) {
						t.Fatalf("log carries the sent value %q: %s", f, log)
					}
				}
			}
			known := tt.reason == ReasonTokenRevoked || tt.reason == ReasonTokenExpired || tt.reason == ReasonTokenOwnerInactive
			if has := strings.Contains(log, "tid="+tTokenID) && strings.Contains(log, "user_id="+tOwnerID); has != (known || tt.dbLookups == 1 && tt.status == 403) {
				t.Fatalf("identity in log = %v for reason %s: %s", has, tt.reason, log)
			}
			if known && !strings.Contains(log, "level=WARN") {
				t.Fatalf("a known token that fails must log at WARN: %s", log)
			}
		})
	}
}

// An app/initiative denial of a token is recorded like a session one (owner as user, no sid, once per cache miss);
// a scope denial (a ceiling, not an access rule) is not.
func TestTokenPathRecordsDenial(t *testing.T) {
	secret := newSecret(t)
	rig := newTokenRig(t, liveRecord(assertion.ScopeAdmin), secret)
	rig.eval.denyReason = access.ReasonInitiativeInactive
	den := &recordingDenials{}
	rig.h.cfg.Denials = den
	for range 2 {
		if w := rig.serve(t, tReq{host: tPortalHost, authz: []string{"Bearer " + secret}}); w.Code != http.StatusForbidden {
			t.Fatalf("status %d", w.Code)
		}
	}
	if len(den.rows) != 1 || den.rows[0].UserID != tOwnerID || den.rows[0].SessionID != "" ||
		den.rows[0].Host != tPortalHost || den.rows[0].Reason != access.ReasonInitiativeInactive {
		t.Fatalf("denials = %+v, want one row for the owner without sid", den.rows)
	}

	rig = newTokenRig(t, liveRecord(assertion.ScopeRead), secret)
	den = &recordingDenials{}
	rig.h.cfg.Denials = den
	rig.serve(t, tReq{host: tPortalHost, method: http.MethodPost, authz: []string{"Bearer " + secret}})
	if len(den.rows) != 0 {
		t.Fatalf("scope denial recorded: %+v", den.rows)
	}
}

// Without X-Forwarded-Method a read token fails closed (it must not default to GET); admin has no method limit.
func TestTokenPathMissingForwardedMethod(t *testing.T) {
	secret := newSecret(t)
	for scope, want := range map[string]int{"read": http.StatusForbidden, "admin": http.StatusOK} {
		rig := newTokenRig(t, liveRecord(scope), secret)
		w := rig.serve(t, tReq{host: tPortalHost, authz: []string{"Bearer " + secret}})
		if w.Code != want {
			t.Fatalf("%s: status %d, want %d", scope, w.Code, want)
		}
		if scope == "read" && w.Header().Get("WWW-Authenticate") != challengeInsufficientScope {
			t.Fatalf("challenge %q", w.Header().Get("WWW-Authenticate"))
		}
	}
}

func TestTokenPathScopeByMethod(t *testing.T) {
	secret := newSecret(t)
	for _, tt := range []struct {
		scope, method string
		want          int
	}{
		{"read", http.MethodGet, 200}, {"read", http.MethodHead, 200}, {"read", "get", 200},
		{"read", http.MethodPost, 403}, {"read", http.MethodPatch, 403}, {"read", http.MethodPut, 403},
		{"admin", http.MethodGet, 200}, {"admin", http.MethodPost, 200}, {"admin", http.MethodDelete, 200},
	} {
		t.Run(tt.scope+" "+tt.method, func(t *testing.T) {
			rig := newTokenRig(t, liveRecord(tt.scope), secret)
			if w := rig.serve(t, tReq{host: tPortalHost, method: tt.method, authz: []string{"Bearer " + secret}}); w.Code != tt.want {
				t.Fatalf("status %d, want %d", w.Code, tt.want)
			}
		})
	}
}

// Authorization is read only on the portal host: any other host ignores it (no token lookup, cookie path as ever).
func TestAuthorizationIsIgnoredOutsideThePortal(t *testing.T) {
	secret := newSecret(t)
	rig := newTokenRig(t, liveRecord(assertion.ScopeAdmin), secret)
	w := rig.serve(t, tReq{host: tOtherHost, authz: []string{"Bearer " + secret}, headers: map[string]string{"Accept": "application/json"}})
	if rig.tokens.calls != 0 || len(rig.issuer.got) != 0 {
		t.Fatalf("token looked up on another host (%d lookups)", rig.tokens.calls)
	}
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("status %d challenge %q, want the cookie-path 401 without a Bearer challenge", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	if !strings.Contains(w.Body.String(), "login_url") {
		t.Fatalf("other host did not take the session path: %s", w.Body)
	}
	if log := rig.logs.String(); strings.Contains(log, secret) {
		t.Fatalf("log carries the token: %s", log)
	}
}

// The preflight keeps winning on the portal host, and no token is read.
func TestPreflightWinsOverTheToken(t *testing.T) {
	rig := newTokenRig(t, liveRecord(assertion.ScopeAdmin), newSecret(t))
	w := rig.serve(t, tReq{
		host: tPortalHost, method: http.MethodOptions, authz: []string{"Basic garbage"},
		headers: map[string]string{"Origin": "https://x.example.test", headerACRM: "POST"},
	})
	if w.Code != http.StatusOK || w.Header().Get(HeaderAssertion) != "" || w.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("status %d headers %v", w.Code, w.Header())
	}
	if rig.tokens.calls != 0 || rig.obs.got[0].reason != ReasonPreflight {
		t.Fatalf("lookups %d decisions %v", rig.tokens.calls, rig.obs.got)
	}
	// An OPTIONS that is not a preflight (no Origin) takes the token path like any other request.
	if w := rig.serve(t, tReq{host: tPortalHost, method: http.MethodOptions, authz: []string{"Basic garbage"}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("plain OPTIONS status %d, want 401", w.Code)
	}
}

func TestTokenPathDatabaseFailureFailsClosed(t *testing.T) {
	secret := newSecret(t)
	rig := newTokenRig(t, liveRecord(assertion.ScopeAdmin), secret)
	rig.tokens.err = errors.New("connection refused")
	w := rig.serve(t, tReq{host: tPortalHost, authz: []string{"Bearer " + secret}})
	if w.Code != http.StatusServiceUnavailable || w.Header().Get(HeaderAssertion) != "" || w.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("status %d headers %v", w.Code, w.Header())
	}
	if len(rig.obs.got) != 1 || rig.obs.got[0] != (recordedDecision{DecisionError, ReasonBackend}) {
		t.Fatalf("decisions = %v", rig.obs.got)
	}
	if strings.Contains(rig.logs.String(), secret) {
		t.Fatal("log carries the token")
	}
}
