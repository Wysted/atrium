package keys_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"testing"

	"platform/api/internal/keys"
)

// RFC 7515 appendix A.3 ES256 key. The expected thumbprint was computed independently:
// printf '<canonical json>' | sha256sum | xxd -r -p | basenc --base64url | tr -d '='.
const (
	rfc7515D          = "jpsQnnGQmL-YBIffH1136cspYG6-0iY7X1fCE9-E9LI"
	rfc7515X          = "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU"
	rfc7515Y          = "x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0"
	rfc7515Thumbprint = "oKIywvGUpTVTyxMQ3bwIIeQUudfr_CkLMjCE19ECD-U"
)

var kidFormat = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`) // matches the signing_keys CHECK constraint

func rfc7515Key(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	d, err := base64.RawURLEncoding.DecodeString(rfc7515D)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestThumbprintFixedVector(t *testing.T) {
	key := rfc7515Key(t)

	t.Run("from the key", func(t *testing.T) {
		got, err := keys.Thumbprint(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		if got != rfc7515Thumbprint {
			t.Errorf("Thumbprint() = %q, want %q", got, rfc7515Thumbprint)
		}
	})
	t.Run("from the JWK members only", func(t *testing.T) {
		// Extra members (kid, use, alg) must not influence the thumbprint.
		j := keys.JWK{Kty: "EC", Crv: "P-256", X: rfc7515X, Y: rfc7515Y, Kid: "ignored", Use: "sig", Alg: "ES256"}
		if got := j.Thumbprint(); got != rfc7515Thumbprint {
			t.Errorf("JWK.Thumbprint() = %q, want %q", got, rfc7515Thumbprint)
		}
	})
	t.Run("NewJWK", func(t *testing.T) {
		j, err := keys.NewJWK(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		if j.X != rfc7515X || j.Y != rfc7515Y || j.Kid != rfc7515Thumbprint || j.Kty != "EC" || j.Crv != "P-256" || j.Use != "sig" || j.Alg != "ES256" {
			t.Errorf("unexpected JWK: %+v", j)
		}
		if !kidFormat.MatchString(j.Kid) {
			t.Errorf("kid %q does not match the table constraint", j.Kid)
		}
	})
}

func TestNewJWKIsPublicOnlyAndPadsCoordinates(t *testing.T) {
	for range 50 { // coordinates with leading zero bytes appear ~1/128 of the time; the loop exercises padding
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		j, err := keys.NewJWK(&k.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []string{j.X, j.Y} {
			raw, err := base64.RawURLEncoding.DecodeString(c)
			if err != nil || len(raw) != 32 {
				t.Fatalf("coordinate %q decodes to %d bytes (err %v)", c, len(raw), err)
			}
		}
		body, _ := json.Marshal(j)
		var members map[string]any
		if err := json.Unmarshal(body, &members); err != nil {
			t.Fatal(err)
		}
		if _, hasD := members["d"]; hasD {
			t.Fatal("JWK exposes private member d")
		}
	}
}
