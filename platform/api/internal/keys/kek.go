package keys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"platform/api/internal/secretfile"
)

// KEKSize is the key-encryption-key length in bytes (AES-256).
const KEKSize = 32

// sealedVersion is the first byte of every sealed private key. A future KEK or format rotation bumps it
// and Open dispatches on it, so old and new ciphertexts can coexist while keys are re-sealed.
const sealedVersion byte = 1

// kekIDLabel is the domain-separation label of the KEK fingerprint. Bump the version suffix to change the derivation.
const kekIDLabel = "platform/kek-id/v1"

// kekIDSize is how many bytes of the HMAC output form the fingerprint.
const kekIDSize = 16

// KEKFileEnv names the environment variable holding the path of the KEK file mounted from the Kubernetes Secret.
const KEKFileEnv = "PLATFORM_KEK_FILE"

// ErrDecrypt is returned when a sealed private key cannot be opened: wrong KEK, wrong kid (AAD) or tampered data.
var ErrDecrypt = errors.New("decrypt private key")

// KEK is the key-encryption key used for envelope encryption (AES-256-GCM). It never leaves memory:
// it is not stored in the database and its String/GoString forms are redacted.
type KEK struct {
	aead cipher.AEAD
	id   string
}

// deriveKEKID returns the non-reversible fingerprint of a KEK: HMAC-SHA256 keyed with the KEK over a fixed
// domain-separation label, truncated and base64url-encoded. It identifies which KEK sealed a key without
// revealing it, and is not a plain hash of the KEK so it cannot be reused as one.
func deriveKEKID(raw []byte) string {
	mac := hmac.New(sha256.New, raw)
	mac.Write([]byte(kekIDLabel))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:kekIDSize])
}

// ID returns the KEK fingerprint stored next to every key sealed with it.
func (k *KEK) ID() string { return k.id }

// NewKEK builds a KEK from exactly KEKSize raw bytes.
func NewKEK(raw []byte) (*KEK, error) {
	if len(raw) != KEKSize {
		return nil, fmt.Errorf("KEK must be %d bytes, got %d", KEKSize, len(raw))
	}
	if subtle.ConstantTimeCompare(raw, make([]byte, len(raw))) == 1 {
		return nil, errors.New("KEK must not be all zeros")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("init KEK cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init KEK gcm: %w", err)
	}
	return &KEK{aead: aead, id: deriveKEKID(raw)}, nil
}

// LoadKEKFile reads a raw 32-byte KEK from path. The file must be a regular file whose permissions are
// no looser than 0640 (see secretfile.Read; group write and any "other" bit are rejected, not just warned about).
func LoadKEKFile(path string) (*KEK, error) {
	raw, err := secretfile.Read(path, KEKSize)
	if err != nil {
		return nil, fmt.Errorf("KEK file: %w", err)
	}
	defer clear(raw)
	return NewKEK(raw)
}

// GenerateKEK returns a random KEK together with its raw bytes (for tests and bootstrap tooling).
func GenerateKEK() (*KEK, []byte, error) {
	raw := make([]byte, KEKSize)
	if _, err := rand.Read(raw); err != nil {
		return nil, nil, fmt.Errorf("random KEK: %w", err)
	}
	kek, err := NewKEK(raw)
	return kek, raw, err
}

// String and GoString keep the KEK out of logs and %v / %#v dumps.
func (*KEK) String() string   { return "KEK(redacted)" }
func (*KEK) GoString() string { return "KEK(redacted)" }

// Seal encrypts plaintext with a fresh random nonce. The kid is bound as AAD so the ciphertext only
// opens under the key it was created for. The output is version || nonce || ciphertext || tag.
func (k *KEK) Seal(kid string, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("random nonce: %w", err)
	}
	out := append(make([]byte, 0, 1+len(nonce)+len(plaintext)+k.aead.Overhead()), sealedVersion)
	out = append(out, nonce...)
	return k.aead.Seal(out, nonce, plaintext, sealedAAD(sealedVersion, kid)), nil
}

// sealedAAD binds both the format version and the kid, so neither can be swapped without detection.
func sealedAAD(version byte, kid string) []byte {
	return append([]byte{version}, kid...)
}

// Open reverses Seal; kid must equal the one used to seal.
func (k *KEK) Open(kid string, sealed []byte) ([]byte, error) {
	if len(sealed) < 1+k.aead.NonceSize()+k.aead.Overhead() {
		return nil, ErrDecrypt
	}
	version, body := sealed[0], sealed[1:]
	if version != sealedVersion {
		return nil, fmt.Errorf("%w: unsupported sealed format version %d", ErrDecrypt, version)
	}
	nonce, ct := body[:k.aead.NonceSize()], body[k.aead.NonceSize():]
	plaintext, err := k.aead.Open(nil, nonce, ct, sealedAAD(version, kid))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

// LoadKEKFromEnv loads the KEK from the file named by PLATFORM_KEK_FILE.
func LoadKEKFromEnv() (*KEK, error) {
	path := os.Getenv(KEKFileEnv)
	if path == "" {
		return nil, fmt.Errorf("%s is not set", KEKFileEnv)
	}
	return LoadKEKFile(path)
}
