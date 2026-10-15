package appauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"spike/forwardauth/internal/token"
)

const kid = "k1"

var now = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func genKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func claims(aud string, exp time.Time) token.Claims {
	return token.Claims{
		Roles: []string{"user"},
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: token.Issuer, Subject: "uuid-1", Audience: jwt.ClaimStrings{aud},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
}

func signWith(t *testing.T, m jwt.SigningMethod, key any, c token.Claims) string {
	t.Helper()
	tok := jwt.NewWithClaims(m, c)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMiddleware(t *testing.T) {
	key, other := genKey(t), genKey(t)
	jwk, err := token.NewJWK(kid, &key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier("app2", token.Set{Keys: []token.JWK{jwk}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	h := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	valid := now.Add(90 * time.Second)

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"valid token", signWith(t, jwt.SigningMethodES256, key, claims("app2", valid)), http.StatusOK},
		{"missing token", "", http.StatusUnauthorized},
		{"token minted for app1 used on app2", signWith(t, jwt.SigningMethodES256, key, claims("app1", valid)), http.StatusUnauthorized},
		{"expired", signWith(t, jwt.SigningMethodES256, key, claims("app2", now.Add(-time.Second))), http.StatusUnauthorized},
		{"signed by another key", signWith(t, jwt.SigningMethodES256, other, claims("app2", valid)), http.StatusUnauthorized},
		{"alg none", signWith(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, claims("app2", valid)), http.StatusUnauthorized},
		{"HS256 with public key bytes", signWith(t, jwt.SigningMethodHS256, []byte(jwk.X+jwk.Y), claims("app2", valid)), http.StatusUnauthorized},
		{"garbage", "not.a.jwt", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.token != "" {
				req.Header.Set(token.HeaderName, tt.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
