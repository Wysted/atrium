// Package token holds the JWT contract shared by authz (issuer) and the apps (verifiers).
package token

import (
	"crypto/ecdsa"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// Issuer is the fixed "iss" claim of every portal token.
	Issuer = "portal-authz"
	// Algorithm is the only accepted signing algorithm.
	Algorithm = "ES256"
	// HeaderName is the request header that carries the token to the app.
	HeaderName = "X-Portal-Token"
)

// Claims is the token payload. sub is the internal user UUID, aud the stable app id.
type Claims struct {
	Roles []string `json:"roles"`
	jwt.RegisteredClaims
}

// Sign signs claims with ES256 and tags the token with the key id.
func Sign(key *ecdsa.PrivateKey, kid string, claims Claims) (string, error) {
	t := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	t.Header["kid"] = kid
	s, err := t.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return s, nil
}
