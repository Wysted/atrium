package jwks_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Wysted/atrium/platform/api/internal/assertion"
)

// forgeUnknownKID signs a well-formed assertion with a random key published nowhere, under a made-up kid.
func forgeUnknownKID(t *testing.T, s *stack) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := s.clock.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": itIssuer, "aud": itAudience, "sub": "u", "iat": now.Unix(), "nbf": now.Unix(),
		"exp": now.Add(assertion.TTL).Unix(),
	})
	tok.Header["typ"] = assertion.HeaderType
	tok.Header["kid"] = "invented-kid"
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
