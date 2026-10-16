// Package secretfile reads secrets mounted from files (Kubernetes Secrets) with one permission policy for
// every secret of the process: the KEK and the Google client secret share it.
package secretfile

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// MaxMode is the loosest permission set accepted: owner read/write and group read.
// In Kubernetes a mounted Secret belongs to root and a non-root pod reads it through its group
// (fsGroup + defaultMode 0440), so group read must be allowed; group write and any access for others are not.
const MaxMode os.FileMode = 0o640

// ErrTooLarge means the file holds more than the caller's limit.
var ErrTooLarge = errors.New("secret file too large")

// Read returns the content of the secret file at path (at most maxBytes). The file must be a regular file
// whose permissions are no looser than MaxMode: group write and any "other" bit are rejected, not just
// warned about. The checks run on the open handle, so there is no check/use race.
func Read(path string, maxBytes int) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: path is the operator-configured secret location, never request input
	if err != nil {
		return nil, fmt.Errorf("open secret file: %w", err)
	}
	defer func() { _ = f.Close() }() // read-only handle: a close error cannot lose data
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat secret file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("secret file is not a regular file")
	}
	if info.Mode().Perm()&^MaxMode != 0 {
		return nil, fmt.Errorf("secret file permissions %#o are too open, want %#o or stricter", info.Mode().Perm(), MaxMode)
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read secret file: %w", err)
	}
	if len(raw) > maxBytes {
		clear(raw)
		return nil, ErrTooLarge
	}
	return raw, nil
}
