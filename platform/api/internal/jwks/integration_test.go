package jwks_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"platform/api/internal/assertion"
	"platform/api/internal/jwks"
	"platform/api/internal/keys"
	"platform/api/internal/postgres"
	"platform/api/internal/testdb"
	platformauth "platform/sdk/go"
)

// These tests wire the real stack: PostgreSQL key store -> rotator -> keyring -> issuer -> JWKS handler
// over HTTP -> the SDK verifier that apps use.

const (
	itIssuer   = "https://auth.apps.example.test"
	itAudience = "app_01J0000000000000000000000"
	itTimeout  = 30 * time.Second
)

var itStart = time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type stack struct {
	t       *testing.T
	clock   *clock
	rotator *keys.Rotator
	ring    *keys.Keyring
	issuer  *assertion.Issuer
	server  *httptest.Server
	hits    atomic.Int64
}

func newStack(t *testing.T) *stack {
	t.Helper()
	pool := testdb.New(t)
	kek, _, err := keys.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	s := &stack{t: t, clock: &clock{t: itStart}}
	store := postgres.NewKeyStore(pool, postgres.WithClock(s.clock.Now))
	if s.rotator, err = keys.NewRotator(keys.RotatorConfig{Store: store, KEK: kek}); err != nil {
		t.Fatal(err)
	}
	if s.ring, err = keys.NewKeyring(keys.KeyringConfig{Store: store, KEK: kek}); err != nil {
		t.Fatal(err)
	}
	if s.issuer, err = assertion.NewIssuer(assertion.Config{Issuer: itIssuer, Signers: s.ring, Now: s.clock.Now}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler := jwks.Handler(s.ring)
	mux.HandleFunc(jwks.Route, func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		handler.ServeHTTP(w, r)
	})
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *stack) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), itTimeout)
	s.t.Cleanup(cancel)
	return ctx
}

// tick advances the clock, runs a rotation tick and reloads the keyring (what NOTIFY does in production).
func (s *stack) tick(d time.Duration) keys.TickResult {
	s.t.Helper()
	s.clock.Advance(d)
	res, err := s.rotator.Tick(s.ctx())
	if err != nil {
		s.t.Fatalf("tick: %v", err)
	}
	s.reload()
	return res
}

func (s *stack) reload() {
	s.t.Helper()
	if err := s.ring.Reload(s.ctx()); err != nil {
		s.t.Fatalf("reload: %v", err)
	}
}

func (s *stack) verifier(leeway time.Duration) *platformauth.Verifier {
	s.t.Helper()
	v, err := platformauth.NewVerifier(platformauth.Config{
		Issuer: itIssuer, Audience: itAudience, JWKSURL: s.server.URL + jwks.Path,
		Leeway: leeway, Now: s.clock.Now,
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return v
}

func (s *stack) issue() string {
	s.t.Helper()
	issued, err := s.issuer.Issue(assertion.Request{
		Audience: itAudience, Subject: "0198a1e2-0000-7000-8000-000000000001", Email: "ana@example.test",
		Name: "Ana", SessionID: "0198a1e2-0000-7000-8000-0000000000aa", Roles: []string{"editor"},
	})
	if err != nil {
		s.t.Fatalf("issue: %v", err)
	}
	return issued.Token
}

func (s *stack) activeKID() string {
	s.t.Helper()
	signer, err := s.ring.Signer()
	if err != nil {
		s.t.Fatal(err)
	}
	return signer.KID
}

// jwksKIDs fetches the JWKS over HTTP and returns its kids in served order.
func (s *stack) jwksKIDs() []string {
	s.t.Helper()
	resp, err := http.Get(s.server.URL + jwks.Path)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc struct {
		Keys []struct {
			Kid string `json:"kid"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		s.t.Fatal(err)
	}
	s.hits.Add(-1) // do not count this inspection request
	var kids []string
	for _, k := range doc.Keys {
		kids = append(kids, k.Kid)
	}
	return kids
}

func verify(v *platformauth.Verifier, token string) error {
	_, err := v.Verify(context.Background(), token)
	return err
}

func TestIssuedAssertionVerifiesWithSDK(t *testing.T) {
	s := newStack(t)
	s.tick(0)
	v := s.verifier(0)

	claims, err := v.Verify(context.Background(), s.issue())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Audience != itAudience || claims.Issuer != itIssuer || claims.Subject == "" || !slices.Equal(claims.Roles, []string{"editor"}) {
		t.Errorf("claims = %+v", claims)
	}
	if got := claims.ExpiresAt.Sub(claims.IssuedAt); got != assertion.TTL {
		t.Errorf("lifetime = %v, want %v", got, assertion.TTL)
	}
}

func TestJWKSPhasesDuringFullRotation(t *testing.T) {
	s := newStack(t)

	// bootstrap: the active key plus a next key published from the start
	s.tick(0)
	a := s.activeKID()
	kids := s.jwksKIDs()
	if len(kids) != 2 || kids[0] != a {
		t.Fatalf("bootstrap JWKS = %v, want active %s first plus one next", kids, a)
	}
	b := kids[1]
	if s.activeKID() != a {
		t.Fatal("next key signs")
	}
	assertVerifies(t, s)

	// promotion: b signs, a is retired but still published, a replacement next c appears
	s.tick(keys.ActiveDuration)
	kids = s.jwksKIDs()
	if len(kids) != 3 || kids[0] != b || kids[2] != a {
		t.Fatalf("promote-phase JWKS = %v, want [%s next %s]", kids, b, a)
	}
	c := kids[1]
	if s.activeKID() != b {
		t.Fatal("promoted key does not sign")
	}
	assertVerifies(t, s)

	// removal: a is gone, b and the next c remain
	s.tick(keys.RetiredDuration)
	if got := s.jwksKIDs(); !slices.Equal(got, []string{b, c}) {
		t.Fatalf("removal-phase JWKS = %v, want [%s %s]", got, b, c)
	}
	assertVerifies(t, s)
}

// assertVerifies checks that a freshly issued assertion verifies against a verifier with an empty cache.
func assertVerifies(t *testing.T, s *stack) {
	t.Helper()
	if err := verify(s.verifier(0), s.issue()); err != nil {
		t.Fatalf("fresh assertion rejected: %v", err)
	}
}

func TestTokenSignedByRetiredKeyStaysValid(t *testing.T) {
	s := newStack(t)
	s.tick(0)
	a := s.activeKID()
	v := s.verifier(0) // long-lived verifier, as in a running app

	s.clock.Advance(keys.ActiveDuration - time.Second)
	token := s.issue() // signed by a, 1s before the swap
	if s.activeKID() != a {
		t.Fatal("setup: a should still sign")
	}

	res := s.tick(time.Second) // swap
	if !slices.Equal(kindsOf(res), []keys.ActionKind{keys.ActionRetire, keys.ActionPromote, keys.ActionCreateNext}) {
		t.Fatalf("swap events = %v", res.Events)
	}
	if s.activeKID() == a {
		t.Fatal("setup: a should be retired")
	}
	if err := verify(v, token); err != nil {
		t.Fatalf("token signed by the retired key rejected: %v", err)
	}
	if err := verify(v, s.issue()); err != nil {
		t.Fatalf("token signed by the new active key rejected: %v", err)
	}
}

func kindsOf(res keys.TickResult) []keys.ActionKind {
	var out []keys.ActionKind
	for _, e := range res.Events {
		out = append(out, e.Action)
	}
	return out
}

func TestRemovedKeyRejectedOnceSDKCacheExpires(t *testing.T) {
	s := newStack(t)
	s.tick(0)
	a := s.activeKID()
	// Generous leeway so the token is still time-valid when the cache expires: the rejection must come from the key.
	v := s.verifier(15 * time.Minute)

	token := s.issue()
	if err := verify(v, token); err != nil {
		t.Fatal(err)
	}

	if err := s.rotator.Revoke(s.ctx(), a); err != nil {
		t.Fatal(err)
	}
	s.reload()
	if kids := s.jwksKIDs(); slices.Contains(kids, a) {
		t.Fatalf("revoked key %s still in the JWKS", a)
	}

	// Documented window: apps keep accepting the key until their cache expires.
	if err := verify(v, token); err != nil {
		t.Fatalf("cached key should still verify inside the max-age window: %v", err)
	}

	// Stale-while-revalidate: the first request after expiry may still be served from the stale key while
	// the refresh runs in the background; once it has landed the removed key is rejected.
	s.clock.Advance(5*time.Minute + time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := verify(v, token)
		if errors.Is(err, platformauth.ErrInvalidAssertion) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("err = %v, want rejection after the cache expired and refreshed", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNewKIDRefetchesOnceAndInventedKIDsAreRateLimited(t *testing.T) {
	s := newStack(t)
	s.tick(0)
	a := s.activeKID()
	v := s.verifier(0)

	if err := verify(v, s.issue()); err != nil {
		t.Fatal(err)
	}
	if got := s.hits.Load(); got != 1 {
		t.Fatalf("initial hits = %d, want 1", got)
	}

	// Emergency rotation promotes the next key apps already fetched with the JWKS: no refetch needed.
	if err := s.rotator.Revoke(s.ctx(), a); err != nil {
		t.Fatal(err)
	}
	s.reload()
	if err := verify(v, s.issue()); err != nil {
		t.Fatalf("promoted next key rejected: %v", err)
	}
	if got := s.hits.Load(); got != 1 {
		t.Fatalf("hits after promoting a pre-published key = %d, want 1 (no refetch)", got)
	}

	// A second emergency rotation promotes the replacement next, which the cached JWKS predates:
	// a kid apps have never seen, fetched exactly once.
	if err := s.rotator.Revoke(s.ctx(), s.activeKID()); err != nil {
		t.Fatal(err)
	}
	s.reload()
	s.clock.Advance(platformauth.DefaultMinRefetchInterval + time.Second)

	newToken := s.issue()
	if err := verify(v, newToken); err != nil {
		t.Fatalf("new kid rejected: %v", err)
	}
	if got := s.hits.Load(); got != 2 {
		t.Fatalf("hits after new kid = %d, want exactly 2", got)
	}
	if err := verify(v, newToken); err != nil || s.hits.Load() != 2 {
		t.Fatalf("known kid caused a fetch (hits %d, err %v)", s.hits.Load(), err)
	}

	forged := forgeUnknownKID(t, s)
	for range 5 {
		if err := verify(v, forged); err == nil {
			t.Fatal("token with an invented kid accepted")
		}
	}
	if got := s.hits.Load(); got != 2 {
		t.Fatalf("invented kid triggered refetches: hits = %d, want 2", got)
	}

	s.clock.Advance(platformauth.DefaultMinRefetchInterval + time.Second)
	forged = forgeUnknownKID(t, s) // fresh timestamps
	for range 5 {
		_ = verify(v, forged)
	}
	if got := s.hits.Load(); got != 3 {
		t.Fatalf("hits = %d, want 3 (one refetch per interval)", got)
	}
}
