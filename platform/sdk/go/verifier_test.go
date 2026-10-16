package platformauth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	platformauth "platform/sdk/go"
)

func TestVerify(t *testing.T) {
	signer := newKey(t)
	other := newKey(t)

	tests := []struct {
		name    string
		header  map[string]any
		mutate  func(c jwt.MapClaims)
		key     *ecdsa.PrivateKey
		wantErr bool
	}{
		{name: "valid"},
		{name: "expired within leeway", mutate: func(c jwt.MapClaims) { c["exp"] = baseTime.Add(-10 * time.Second).Unix() }},
		{name: "expired beyond leeway", mutate: func(c jwt.MapClaims) { c["exp"] = baseTime.Add(-60 * time.Second).Unix() }, wantErr: true},
		{name: "nbf in future within leeway", mutate: func(c jwt.MapClaims) { c["nbf"] = baseTime.Add(20 * time.Second).Unix() }},
		{name: "nbf in future beyond leeway", mutate: func(c jwt.MapClaims) { c["nbf"] = baseTime.Add(60 * time.Second).Unix() }, wantErr: true},
		{name: "iat in future beyond leeway", mutate: func(c jwt.MapClaims) { c["iat"] = baseTime.Add(60 * time.Second).Unix() }, wantErr: true},
		{name: "missing exp", mutate: func(c jwt.MapClaims) { delete(c, "exp") }, wantErr: true},
		{name: "missing aud", mutate: func(c jwt.MapClaims) { delete(c, "aud") }, wantErr: true},
		{name: "wrong aud", mutate: func(c jwt.MapClaims) { c["aud"] = "app_other" }, wantErr: true},
		{name: "aud array with extra audience", mutate: func(c jwt.MapClaims) { c["aud"] = []string{testAudience, "app_other"} }, wantErr: true},
		{name: "aud array with only our app", mutate: func(c jwt.MapClaims) { c["aud"] = []string{testAudience} }},
		{name: "wrong iss", mutate: func(c jwt.MapClaims) { c["iss"] = "https://evil.example" }, wantErr: true},
		{name: "missing sub", mutate: func(c jwt.MapClaims) { delete(c, "sub") }, wantErr: true},
		{name: "wrong typ", header: map[string]any{"typ": "JWT"}, wantErr: true},
		{name: "missing typ", header: map[string]any{"typ": nil}, wantErr: true},
		{name: "missing kid", header: map[string]any{"kid": nil}, wantErr: true},
		{name: "unknown kid", header: map[string]any{"kid": "nope"}, wantErr: true},
		{name: "signature by another key", key: other, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: signer})
			clock := newClock()
			v := newVerifier(t, srv, clock)

			claims := baseClaims(clock.Now())
			if tt.mutate != nil {
				tt.mutate(claims)
			}
			key := signer
			if tt.key != nil {
				key = tt.key
			}
			got, err := v.Verify(context.Background(), mint(t, key, tt.header, claims))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Verify() err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				if !errors.Is(err, platformauth.ErrInvalidAssertion) {
					t.Errorf("error %v does not wrap ErrInvalidAssertion", err)
				}
				return
			}
			if got.Subject != "0198a1e2-0000-7000-8000-000000000001" || got.Audience != testAudience || got.SessionID != "sid-1" ||
				got.TokenID != "jti-1" || len(got.Roles) != 1 || got.Roles[0] != "editor" || got.Email != "ana@example.test" {
				t.Errorf("unexpected claims: %+v", got)
			}
		})
	}
}

func TestVerifyRejectsForeignAlgorithms(t *testing.T) {
	signer := newKey(t)
	tests := []struct {
		name string
		make func(t *testing.T, claims jwt.MapClaims) string
	}{
		{"alg none", func(t *testing.T, c jwt.MapClaims) string {
			tok := jwt.NewWithClaims(jwt.SigningMethodNone, c)
			tok.Header["typ"], tok.Header["kid"] = "platform-assertion+jwt", testKID
			s, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}},
		{"alg HS256", func(t *testing.T, c jwt.MapClaims) string {
			tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
			tok.Header["typ"], tok.Header["kid"] = "platform-assertion+jwt", testKID
			s, err := tok.SignedString([]byte("shared-secret"))
			if err != nil {
				t.Fatal(err)
			}
			return s
		}},
		{"alg ES384", func(t *testing.T, c jwt.MapClaims) string {
			tok := jwt.NewWithClaims(jwt.SigningMethodES384, c)
			tok.Header["typ"], tok.Header["kid"] = "platform-assertion+jwt", testKID
			k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			s, err := tok.SignedString(k)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: signer})
			clock := newClock()
			v := newVerifier(t, srv, clock)
			_, err := v.Verify(context.Background(), tt.make(t, baseClaims(clock.Now())))
			if !errors.Is(err, platformauth.ErrInvalidAssertion) {
				t.Fatalf("err = %v, want ErrInvalidAssertion", err)
			}
			if n := srv.hits.Load(); n != 0 {
				t.Errorf("JWKS fetched %d times for a token rejected on its header", n)
			}
		})
	}
}

func TestVerifyMalformedTokensDoNotFetch(t *testing.T) {
	srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: newKey(t)})
	v := newVerifier(t, srv, newClock())
	for _, tok := range []string{"", "a.b", "a.b.c", "....", "!!!.e30.sig"} {
		if _, err := v.Verify(context.Background(), tok); !errors.Is(err, platformauth.ErrInvalidAssertion) {
			t.Errorf("Verify(%q) err = %v, want ErrInvalidAssertion", tok, err)
		}
	}
	if n := srv.hits.Load(); n != 0 {
		t.Errorf("JWKS fetched %d times", n)
	}
}

func TestVerifyIgnoresJKUAndX5U(t *testing.T) {
	signer := newKey(t)
	var attackerHits int
	attacker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { attackerHits++ }))
	defer attacker.Close()

	srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: signer})
	clock := newClock()
	v := newVerifier(t, srv, clock)
	tok := mint(t, signer, map[string]any{"jku": attacker.URL, "x5u": attacker.URL}, baseClaims(clock.Now()))
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if attackerHits != 0 {
		t.Errorf("jku/x5u URL fetched %d times", attackerHits)
	}
}

func TestKeyCache(t *testing.T) {
	signer := newKey(t)

	verifyAt := func(t *testing.T, v *platformauth.Verifier, c *fakeClock, key *ecdsa.PrivateKey, kid string) error {
		t.Helper()
		_, err := v.Verify(context.Background(), mint(t, key, map[string]any{"kid": kid}, baseClaims(c.Now())))
		return err
	}

	t.Run("max-age is honored and capped at five minutes", func(t *testing.T) {
		tests := []struct {
			name         string
			cacheControl string
			advance      time.Duration
			wantHits     int64
		}{
			{"max-age 60 still fresh", "public, max-age=60", 59 * time.Second, 1},
			{"max-age 60 expired", "public, max-age=60", 61 * time.Second, 2},
			{"large max-age capped, fresh", "public, max-age=3600", 299 * time.Second, 1},
			{"large max-age capped, expired", "public, max-age=3600", 301 * time.Second, 2},
			{"no-store floors the ttl", "no-store", 11 * time.Second, 2},
			{"missing header defaults to the cap", "", 299 * time.Second, 1},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: signer})
				srv.cacheControl = tt.cacheControl
				clock := newClock()
				v := newVerifier(t, srv, clock)
				if err := verifyAt(t, v, clock, signer, testKID); err != nil {
					t.Fatal(err)
				}
				clock.Advance(tt.advance)
				if err := verifyAt(t, v, clock, signer, testKID); err != nil {
					t.Fatal(err)
				}
				waitFetchDone(t, v) // an expired cache is revalidated in the background
				if got := srv.hits.Load(); got != tt.wantHits {
					t.Errorf("hits = %d, want %d", got, tt.wantHits)
				}
			})
		}
	})

	t.Run("removed key is rejected once the cache expires", func(t *testing.T) {
		srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: signer})
		clock := newClock()
		v := newVerifier(t, srv, clock)
		if err := verifyAt(t, v, clock, signer, testKID); err != nil {
			t.Fatal(err)
		}
		srv.dropKey(testKID)
		clock.Advance(100 * time.Second)
		if err := verifyAt(t, v, clock, signer, testKID); err != nil {
			t.Fatalf("cached key must still verify before expiry: %v", err)
		}
		clock.Advance(201 * time.Second)
		// Expired: the stale key serves this request while the refresh runs; afterwards the key is gone.
		if err := verifyAt(t, v, clock, signer, testKID); err != nil {
			t.Fatalf("stale key must serve while revalidating: %v", err)
		}
		waitFetchDone(t, v)
		if err := verifyAt(t, v, clock, signer, testKID); !errors.Is(err, platformauth.ErrInvalidAssertion) {
			t.Fatalf("err = %v, want rejection after expiry", err)
		}
	})

	t.Run("new kid triggers exactly one refetch and invented kids are rate limited", func(t *testing.T) {
		srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: signer})
		clock := newClock()
		v := newVerifier(t, srv, clock)
		if err := verifyAt(t, v, clock, signer, testKID); err != nil {
			t.Fatal(err)
		}
		assertHits := func(want int64) {
			t.Helper()
			if got := srv.hits.Load(); got != want {
				t.Fatalf("hits = %d, want %d", got, want)
			}
		}
		assertHits(1)

		fresh := newKey(t)
		srv.setKey("kid-2", fresh)
		// Inside the minimum interval the unknown kid is rejected without a fetch.
		if err := verifyAt(t, v, clock, fresh, "kid-2"); err == nil {
			t.Fatal("kid-2 accepted before the refetch interval elapsed")
		}
		assertHits(1)

		clock.Advance(platformauth.DefaultMinRefetchInterval + time.Second)
		if err := verifyAt(t, v, clock, fresh, "kid-2"); err != nil {
			t.Fatalf("kid-2 rejected after refetch: %v", err)
		}
		assertHits(2)
		if err := verifyAt(t, v, clock, fresh, "kid-2"); err != nil { // now cached
			t.Fatal(err)
		}
		assertHits(2)

		invented := newKey(t)
		for range 5 {
			if err := verifyAt(t, v, clock, invented, "invented"); err == nil {
				t.Fatal("invented kid accepted")
			}
		}
		assertHits(2) // last fetch is recent: no refetch for invented kids

		clock.Advance(platformauth.DefaultMinRefetchInterval + time.Second)
		for range 5 {
			_ = verifyAt(t, v, clock, invented, "invented")
		}
		assertHits(3) // one refetch per interval, no matter how many tokens
	})

	t.Run("JWKS outage fails closed and failed fetches are rate limited", func(t *testing.T) {
		srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: signer})
		srv.setStatus(http.StatusInternalServerError)
		clock := newClock()
		v := newVerifier(t, srv, clock)
		for range 3 {
			err := verifyAt(t, v, clock, signer, testKID)
			if !errors.Is(err, platformauth.ErrKeysUnavailable) {
				t.Fatalf("err = %v, want ErrKeysUnavailable", err)
			}
		}
		if got := srv.hits.Load(); got != 1 {
			t.Fatalf("hits = %d, want 1", got)
		}
		clock.Advance(platformauth.DefaultMinRefetchInterval + time.Second)
		srv.setStatus(http.StatusOK)
		if err := verifyAt(t, v, clock, signer, testKID); err != nil {
			t.Fatalf("recovery failed: %v", err)
		}
	})

	t.Run("concurrent requests share one fetch", func(t *testing.T) {
		srv := newJWKSServer(t, map[string]*ecdsa.PrivateKey{testKID: signer})
		clock := newClock()
		v := newVerifier(t, srv, clock)
		tok := mint(t, signer, nil, baseClaims(clock.Now()))
		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() {
				if _, err := v.Verify(context.Background(), tok); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if got := srv.hits.Load(); got != 1 {
			t.Fatalf("hits = %d, want 1", got)
		}
	})
}

func TestNewVerifierValidatesConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  platformauth.Config
	}{
		{"missing issuer", platformauth.Config{Audience: "a", JWKSURL: "http://x/jwks"}},
		{"missing audience", platformauth.Config{Issuer: "i", JWKSURL: "http://x/jwks"}},
		{"relative url", platformauth.Config{Issuer: "i", Audience: "a", JWKSURL: "/jwks"}},
		{"non-http scheme", platformauth.Config{Issuer: "i", Audience: "a", JWKSURL: "file:///jwks"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := platformauth.NewVerifier(tt.cfg); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
