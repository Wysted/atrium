// Package keys implements the signing key lifecycle of block 3 of the contract: ECDSA P-256 keys
// identified by their RFC 7638 thumbprint, envelope-encrypted private material and automatic rotation.
package keys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

const (
	// JWTAlgorithm is the only JOSE algorithm the platform signs with.
	JWTAlgorithm = "ES256"

	jwkKeyType     = "EC"
	jwkCurve       = "P-256"
	jwkUseSig      = "sig"
	coordinateSize = 32 // bytes of a P-256 field element
)

// JWK is the public JSON Web Key of a signing key (RFC 7517). It never carries private material.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Kid string `json:"kid,omitempty"`
	Use string `json:"use,omitempty"`
	Alg string `json:"alg,omitempty"`
}

// thumbprintInput lists the required members in lexicographic order (RFC 7638 section 3.2);
// encoding/json emits struct fields in declaration order without whitespace.
type thumbprintInput struct {
	Crv string `json:"crv"`
	Kty string `json:"kty"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// Thumbprint returns the RFC 7638 JWK thumbprint: base64url (no padding) of the SHA-256 of the canonical JSON.
func (j JWK) Thumbprint() string {
	canonical, err := json.Marshal(thumbprintInput{Crv: j.Crv, Kty: j.Kty, X: j.X, Y: j.Y})
	if err != nil { // unreachable: a struct of strings always marshals
		panic(fmt.Sprintf("marshal thumbprint input: %v", err))
	}
	sum := sha256.Sum256(canonical)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// NewJWK builds the public JWK of pub, with kid set to its thumbprint.
func NewJWK(pub *ecdsa.PublicKey) (JWK, error) {
	raw, err := pub.Bytes() // uncompressed point: 0x04 || X || Y
	if err != nil {
		return JWK{}, fmt.Errorf("encode public key: %w", err)
	}
	if pub.Curve != elliptic.P256() || len(raw) != 1+2*coordinateSize {
		return JWK{}, fmt.Errorf("unsupported key: want %s", jwkCurve)
	}
	j := JWK{
		Kty: jwkKeyType,
		Crv: jwkCurve,
		X:   base64.RawURLEncoding.EncodeToString(raw[1 : 1+coordinateSize]),
		Y:   base64.RawURLEncoding.EncodeToString(raw[1+coordinateSize:]),
		Use: jwkUseSig,
		Alg: JWTAlgorithm,
	}
	j.Kid = j.Thumbprint()
	return j, nil
}

// Thumbprint returns the kid of pub.
func Thumbprint(pub *ecdsa.PublicKey) (string, error) {
	j, err := NewJWK(pub)
	if err != nil {
		return "", err
	}
	return j.Kid, nil
}
