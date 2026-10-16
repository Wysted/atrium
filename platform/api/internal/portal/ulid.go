package portal

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"
)

// App ids are "app_" plus a ULID (https://github.com/ulid/spec): 48 bits of Unix milliseconds followed by 80
// random bits, written as 26 Crockford base32 characters. The schema CHECK is ^app_[0-9A-HJKMNP-TV-Z]{26}$.
// A small implementation of the one operation needed (generate) is preferred over a dependency: there is no
// parsing, no monotonic mode and no format to keep in sync with anything but that CHECK.
const (
	appIDPrefix = "app_"
	// crockfordAlphabet is Crockford base32 in uppercase (no I, L, O, U).
	crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	ulidTimeChars     = 10 // 48 bits
	ulidRandomChars   = 16 // 80 bits
	ulidRandomBytes   = 10
	bitsPerChar       = 5
	charMask          = 1<<bitsPerChar - 1
	// maxULIDTime is the largest timestamp a ULID holds: 2^48-1 ms.
	maxULIDTime = 1<<48 - 1
	// randomGroupBytes is how many random bytes feed one group of 8 characters (40 bits).
	randomGroupBytes = 5
	randomGroupChars = 8
)

var errULIDTime = errors.New("portal: timestamp does not fit a ULID")

// newAppID returns a fresh app id for the instant at, drawing the random part from entropy (crypto/rand in
// production).
func newAppID(at time.Time, entropy io.Reader) (string, error) {
	ms := at.UnixMilli()
	if ms < 0 || ms > maxULIDTime {
		return "", errULIDTime
	}
	var random [ulidRandomBytes]byte
	if _, err := io.ReadFull(entropy, random[:]); err != nil {
		return "", fmt.Errorf("read entropy for an app id: %w", err)
	}

	out := make([]byte, 0, len(appIDPrefix)+ulidTimeChars+ulidRandomChars)
	out = append(out, appIDPrefix...)
	var timePart [ulidTimeChars]byte
	for i := ulidTimeChars - 1; i >= 0; i-- {
		timePart[i] = crockfordAlphabet[ms&charMask]
		ms >>= bitsPerChar
	}
	out = append(out, timePart[:]...)
	for group := range ulidRandomBytes / randomGroupBytes {
		var v uint64
		for _, b := range random[group*randomGroupBytes : (group+1)*randomGroupBytes] {
			v = v<<8 | uint64(b)
		}
		var chars [randomGroupChars]byte
		for i := randomGroupChars - 1; i >= 0; i-- {
			chars[i] = crockfordAlphabet[v&charMask]
			v >>= bitsPerChar
		}
		out = append(out, chars[:]...)
	}
	return string(out), nil
}

// generateAppID is newAppID with the system entropy.
func generateAppID(at time.Time) (string, error) { return newAppID(at, rand.Reader) }
