package keys_test

import (
	"crypto/rand"
)

// randReader is crypto/rand.Reader behind a local name to keep imports in tests tidy.
type randReader struct{}

func (randReader) Read(p []byte) (int, error) { return rand.Read(p) }
