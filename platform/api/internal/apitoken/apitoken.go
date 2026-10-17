// Package apitoken defines the secret of an API token (contract block 6, "Non-browser clients"): how it is
// generated, how its format is checked without touching the database, and how it is hashed for storage. The
// portal issues tokens and platform-api's ForwardAuth checks them, so both share this one definition.
//
// A secret is "ptk_" + 43 characters of base64url (32 random bytes) + 8 hex digits of the CRC-32 (IEEE) of the
// preceding text: 55 characters. The prefix lets secret scanners find leaks. The checksum is only a format check
// (it is not secret and the hash covers the whole string), so malformed values are rejected before any query.
package apitoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"hash/crc32"
)

// Layout of a secret.
const (
	Prefix = "ptk_"

	randomBytes   = 32
	randomChars   = 43 // base64.RawURLEncoding of randomBytes
	checksumChars = 8  // CRC-32 as lowercase hex

	// Length is the length of every well-formed secret.
	Length = len(Prefix) + randomChars + checksumChars

	// HashSize is the size of Hash's result (SHA-256).
	HashSize = sha256.Size
)

// Generate returns a new secret from crypto/rand. A failure of the random source is returned, never swallowed:
// a predictable token would be a vulnerability.
func Generate() (string, error) {
	raw := make([]byte, randomBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return withChecksum(Prefix + base64.RawURLEncoding.EncodeToString(raw)), nil
}

// withChecksum appends the CRC-32 of body as eight lowercase hex digits.
func withChecksum(body string) string {
	return body + fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(body)))
}

// WellFormed reports whether s has the layout of a secret: prefix, length, alphabet and checksum. It says
// nothing about whether the token exists, and it is not a secret comparison (the checksum is public).
func WellFormed(s string) bool {
	if len(s) != Length || s[:len(Prefix)] != Prefix {
		return false
	}
	body, sum := s[:Length-checksumChars], s[Length-checksumChars:]
	for i := len(Prefix); i < len(body); i++ {
		if !isBase64URL(body[i]) {
			return false
		}
	}
	for i := range len(sum) {
		if !isLowerHex(sum[i]) {
			return false
		}
	}
	return withChecksum(body) == s
}

func isBase64URL(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

func isLowerHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' }

// Hash returns the SHA-256 of the whole secret: the value stored in api_tokens.token_hash and the key of every
// lookup, so no secret is ever compared. 256 bits of entropy make a slow hash pointless.
func Hash(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

// Record is the token row found by hash together with its owner, as ForwardAuth needs it. The flags are computed
// by the database at the time of the query (now() is the database clock), so a skewed replica cannot extend a token.
type Record struct {
	ID          string // tid claim
	Scope       string
	OwnerID     string
	OwnerKind   string // users.kind: human or robot
	OwnerEmail  string // "" for robots
	OwnerName   string
	Revoked     bool
	Expired     bool
	OwnerActive bool
}

// Usable reports whether the token authenticates a request: not revoked, not expired, owner active.
func (r Record) Usable() bool { return !r.Revoked && !r.Expired && r.OwnerActive }
