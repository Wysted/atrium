package platformauth

import "time"

// SetFetchTimeout shortens the JWKS fetch timeout; call it before the first Verify.
func SetFetchTimeout(v *Verifier, d time.Duration) { v.keys.fetchTimeout = d }

// FetchInflight reports whether a JWKS fetch goroutine is still registered.
func FetchInflight(v *Verifier) bool {
	v.keys.mu.Lock()
	defer v.keys.mu.Unlock()
	return v.keys.inflight != nil
}
