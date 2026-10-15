package token

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	coordLen     = 32 // P-256 coordinate size in bytes
	uncompressed = 0x04
	maxJWKSBytes = 1 << 20
	kidHashBytes = 8
	curveName    = "P-256"
	keyTypeEC    = "EC"
	keyUseSig    = "sig"
)

// JWK is a public EC key in JWK form (RFC 7517).
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// Set is a JWKS document.
type Set struct {
	Keys []JWK `json:"keys"`
}

// KeyID derives a stable key id from the public key.
func KeyID(pub *ecdsa.PublicKey) (string, error) {
	raw, err := pub.Bytes()
	if err != nil {
		return "", fmt.Errorf("encode public key: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:kidHashBytes]), nil
}

// NewJWK encodes a P-256 public key.
func NewJWK(kid string, pub *ecdsa.PublicKey) (JWK, error) {
	raw, err := pub.Bytes()
	if err != nil {
		return JWK{}, fmt.Errorf("encode public key: %w", err)
	}
	if len(raw) != 1+2*coordLen {
		return JWK{}, errors.New("not a P-256 key")
	}
	enc := base64.RawURLEncoding
	return JWK{
		Kty: keyTypeEC, Crv: curveName, Kid: kid, Use: keyUseSig, Alg: Algorithm,
		X: enc.EncodeToString(raw[1 : 1+coordLen]),
		Y: enc.EncodeToString(raw[1+coordLen:]),
	}, nil
}

// PublicKey decodes and validates the key (point must be on the curve).
func (k JWK) PublicKey() (*ecdsa.PublicKey, error) {
	if k.Kty != keyTypeEC || k.Crv != curveName {
		return nil, fmt.Errorf("unsupported key %s/%s", k.Kty, k.Crv)
	}
	enc := base64.RawURLEncoding
	x, err := enc.DecodeString(k.X)
	if err != nil || len(x) != coordLen {
		return nil, errors.New("invalid x coordinate")
	}
	y, err := enc.DecodeString(k.Y)
	if err != nil || len(y) != coordLen {
		return nil, errors.New("invalid y coordinate")
	}
	raw := append([]byte{uncompressed}, x...)
	raw = append(raw, y...)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	return pub, nil
}

// Fetch downloads a JWKS, retrying up to attempts times with delay between tries.
func Fetch(ctx context.Context, client *http.Client, url string, attempts int, delay time.Duration) (Set, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		set, err := fetchOnce(ctx, client, url)
		if err == nil {
			return set, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return Set{}, fmt.Errorf("fetch jwks: %w", ctx.Err())
		case <-time.After(delay):
		}
	}
	return Set{}, fmt.Errorf("fetch jwks after %d attempts: %w", attempts, lastErr)
}

func fetchOnce(ctx context.Context, client *http.Client, url string) (Set, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Set{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Set{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Set{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var set Set
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&set); err != nil {
		return Set{}, fmt.Errorf("decode jwks: %w", err)
	}
	return set, nil
}
