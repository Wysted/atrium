package platformauth_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	platformauth "github.com/Wysted/atrium/platform/sdk/go"
)

const (
	testIssuer   = "https://auth.apps.example.test"
	testAudience = "app_01J0000000000000000000000"
	testKID      = "kid-1"
)

var baseTime = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// fakeClock is a settable clock safe for concurrent use.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: baseTime} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

// jwksServer serves a mutable key set and counts requests.
type jwksServer struct {
	*httptest.Server
	mu           sync.Mutex
	keys         map[string]*ecdsa.PublicKey
	cacheControl string
	status       int
	hits         atomic.Int64
}

func newJWKSServer(t testing.TB, keys map[string]*ecdsa.PrivateKey) *jwksServer {
	t.Helper()
	s := &jwksServer{keys: map[string]*ecdsa.PublicKey{}, cacheControl: "public, max-age=300", status: http.StatusOK}
	for kid, k := range keys {
		s.keys[kid] = &k.PublicKey
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.status != http.StatusOK {
			w.WriteHeader(s.status)
			return
		}
		doc := struct {
			Keys []map[string]string `json:"keys"`
		}{Keys: []map[string]string{}}
		for kid, pub := range s.keys {
			raw, _ := pub.Bytes()
			doc.Keys = append(doc.Keys, map[string]string{
				"kty": "EC", "crv": "P-256", "kid": kid, "use": "sig", "alg": "ES256",
				"x": base64.RawURLEncoding.EncodeToString(raw[1:33]),
				"y": base64.RawURLEncoding.EncodeToString(raw[33:]),
			})
		}
		w.Header().Set("Cache-Control", s.cacheControl)
		w.Header().Set("Content-Type", "application/jwk-set+json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksServer) setKey(kid string, k *ecdsa.PrivateKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[kid] = &k.PublicKey
}

func (s *jwksServer) dropKey(kid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, kid)
}

func (s *jwksServer) setStatus(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = code
}

func (s *jwksServer) URLPath() string { return s.URL + "/.well-known/jwks.json" }

func baseClaims(now time.Time) jwt.MapClaims {
	return jwt.MapClaims{
		"iss": testIssuer, "aud": testAudience, "sub": "0198a1e2-0000-7000-8000-000000000001",
		"email": "ana@example.test", "name": "Ana", "iat": now.Unix(), "nbf": now.Unix(),
		"exp": now.Add(90 * time.Second).Unix(), "jti": "jti-1", "sid": "sid-1", "roles": []string{"editor"},
	}
}

// mint signs claims with ES256 and the standard header; a nil header value deletes the member.
func mint(t testing.TB, key *ecdsa.PrivateKey, header map[string]any, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["typ"] = "platform-assertion+jwt"
	tok.Header["kid"] = testKID
	applyHeader(tok, header)
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func applyHeader(tok *jwt.Token, header map[string]any) {
	for k, v := range header {
		if v == nil {
			delete(tok.Header, k)
		} else {
			tok.Header[k] = v
		}
	}
}

func newVerifier(t testing.TB, srv *jwksServer, clock *fakeClock) *platformauth.Verifier {
	t.Helper()
	v, err := platformauth.NewVerifier(platformauth.Config{
		Issuer: testIssuer, Audience: testAudience, JWKSURL: srv.URLPath(), Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	return v
}
