package platformauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// MaxCacheTTL caps how long a fetched JWKS is trusted, whatever max-age says (contract: 5 minutes).
	MaxCacheTTL = 5 * time.Minute
	// minCacheTTL floors the TTL so max-age=0 or no-store cannot turn every request into a fetch.
	minCacheTTL = 10 * time.Second
	// MaxStaleness is how long after expiry the last keys keep being used when the JWKS cannot be refetched.
	MaxStaleness = 15 * time.Minute
	// FailureBackoff is how long a failed fetch is remembered before the next attempt is allowed.
	FailureBackoff = 2 * time.Second
	// defaultFetchTimeout bounds one JWKS fetch, whatever the caller's context or HTTP client say.
	defaultFetchTimeout = 10 * time.Second
	// maxJWKSBytes bounds the JWKS response body.
	maxJWKSBytes = 1 << 20

	jwkCoordinateSize = 32
	jwkKeyType        = "EC"
	jwkCurve          = "P-256"
)

var (
	// ErrKeysUnavailable means the JWKS could not be fetched or parsed.
	ErrKeysUnavailable = errors.New("signing keys unavailable")
	// errUnknownKID means the kid is not in the JWKS even after the permitted refetch.
	errUnknownKID = errors.New("unknown kid")
)

type jwkSet struct {
	Keys []struct {
		Kty string `json:"kty"`
		Crv string `json:"crv"`
		X   string `json:"x"`
		Y   string `json:"y"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		Alg string `json:"alg"`
	} `json:"keys"`
}

// cacheState is an immutable snapshot of the last successful fetch.
type cacheState struct {
	keys      map[string]*ecdsa.PublicKey
	expiresAt time.Time
}

// flight is one in-progress JWKS fetch shared by every request that needs it (single flight).
// state and err are written before done is closed and read only after.
type flight struct {
	done  chan struct{}
	state cacheState
	err   error
}

// keyCache caches the JWKS and decides when a refetch is allowed.
//
// A fetch never runs under the context of the request that triggered it: that request may be
// canceled by its client, and such a cancellation must neither fail the fetch for everybody else
// nor be remembered as a JWKS failure. Requests wait for the shared fetch with their own context.
type keyCache struct {
	url        string
	client     *http.Client
	now        func() time.Time
	minRefetch time.Duration
	// fetchTimeout bounds one fetch; fixed after construction (tests shorten it through export_test.go).
	fetchTimeout time.Duration

	mu          sync.Mutex // guards everything below
	state       cacheState
	inflight    *flight
	lastAttempt time.Time // END of the last fetch, successful or not (a hung fetch must not eat its own backoff)
	lastErr     error     // error of the last fetch; nil if it succeeded
}

// freshKey returns the key for kid if the cache has not expired. Caller holds mu.
func (c *keyCache) freshKey(now time.Time, kid string) (*ecdsa.PublicKey, bool) {
	if !now.Before(c.state.expiresAt) {
		return nil, false
	}
	k, ok := c.state.keys[kid]
	return k, ok
}

// staleKey returns the key for kid from an expired cache, for at most MaxStaleness after expiry
// (stale-if-error: used only when a refetch is not possible or failed). Caller holds mu.
func (c *keyCache) staleKey(now time.Time, kid string) (*ecdsa.PublicKey, bool) {
	if c.state.keys == nil || !now.Before(c.state.expiresAt.Add(MaxStaleness)) {
		return nil, false
	}
	k, ok := c.state.keys[kid]
	return k, ok
}

// mayFetch applies the refetch rules. Caller holds mu.
//   - after a failed fetch: retry once the short FailureBackoff has passed since it ENDED;
//   - expired cache: refetch;
//   - fresh cache (unknown kid): refetch only if the last attempt is older than minRefetch.
func (c *keyCache) mayFetch(now time.Time) bool {
	if c.lastAttempt.IsZero() {
		return true
	}
	since := now.Sub(c.lastAttempt)
	switch {
	case c.lastErr != nil:
		return since >= FailureBackoff
	case now.Before(c.state.expiresAt):
		return since >= c.minRefetch
	default:
		return true
	}
}

// lookup returns the key for kid.
//   - fresh cache and known kid: no network;
//   - expired cache but the kid is in it within MaxStaleness: the stale key is returned at once
//     (stale-while-revalidate) and, if the rules allow it (see mayFetch), a single-flight refresh runs
//     in the background; a hung or failing JWKS never delays a request that has a usable key;
//   - otherwise the caller waits for the shared fetch (started if allowed);
//   - without a usable key the result is ErrKeysUnavailable, or errUnknownKID when the JWKS answered
//     (or no refetch was attempted yet) and does not have the kid.
//
// ctx only bounds how long this caller waits; abandoning the wait does not cancel the fetch.
func (c *keyCache) lookup(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	c.mu.Lock()
	now := c.now()
	if k, ok := c.freshKey(now, kid); ok {
		c.mu.Unlock()
		return k, nil
	}
	stale, haveStale := c.staleKey(now, kid)
	f := c.inflight
	if f == nil {
		if !c.mayFetch(now) {
			if haveStale {
				c.mu.Unlock()
				return stale, nil
			}
			k, err := c.fallback(now, kid, c.lastErr)
			c.mu.Unlock()
			return k, err
		}
		f = &flight{done: make(chan struct{})}
		c.inflight = f
		go c.runFetch(context.WithoutCancel(ctx), f, now)
	}
	c.mu.Unlock()
	if haveStale {
		return stale, nil
	}

	select {
	case <-f.done:
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: waiting for JWKS: %w", ErrKeysUnavailable, ctx.Err())
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if f.err == nil {
		if k, ok := f.state.keys[kid]; ok {
			return k, nil
		}
		return nil, errUnknownKID
	}
	return c.fallback(c.now(), kid, f.err)
}

// fallback resolves kid when no fetch result is available: stale keys first, then the error.
// Caller holds mu.
func (c *keyCache) fallback(now time.Time, kid string, fetchErr error) (*ecdsa.PublicKey, error) {
	if k, ok := c.staleKey(now, kid); ok {
		return k, nil
	}
	if fetchErr == nil && now.Before(c.state.expiresAt) {
		return nil, errUnknownKID // fresh cache without that kid and no refetch allowed yet
	}
	if fetchErr == nil {
		fetchErr = ErrKeysUnavailable
	}
	// A failed refetch means the kid could not be checked: that is an outage (503), not a bad token.
	return nil, fetchErr
}

// runFetch performs the fetch with its own timeout, publishes the result and wakes the waiters.
func (c *keyCache) runFetch(ctx context.Context, f *flight, now time.Time) {
	ctx, cancel := context.WithTimeout(ctx, c.fetchTimeout)
	defer cancel()
	state, err := c.fetch(ctx, now)

	c.mu.Lock()
	c.lastErr = err
	c.lastAttempt = c.now()
	if err == nil {
		c.state = state
	}
	c.inflight = nil
	c.mu.Unlock()

	f.state, f.err = state, err
	close(f.done)
}

func (c *keyCache) fetch(ctx context.Context, now time.Time) (cacheState, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return cacheState{}, fmt.Errorf("%w: %v", ErrKeysUnavailable, err)
	}
	req.Header.Set("Accept", "application/jwk-set+json, application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return cacheState{}, fmt.Errorf("%w: %v", ErrKeysUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Redirects are never followed (see NewVerifier), so a 3xx lands here too.
		return cacheState{}, fmt.Errorf("%w: status %d", ErrKeysUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil || len(body) > maxJWKSBytes {
		return cacheState{}, fmt.Errorf("%w: unreadable or oversized body", ErrKeysUnavailable)
	}
	var set jwkSet
	if err := json.Unmarshal(body, &set); err != nil {
		return cacheState{}, fmt.Errorf("%w: malformed JWKS", ErrKeysUnavailable)
	}

	keys := make(map[string]*ecdsa.PublicKey, len(set.Keys))
	for _, j := range set.Keys {
		if j.Kid == "" || j.Kty != jwkKeyType || j.Crv != jwkCurve {
			continue
		}
		if (j.Alg != "" && j.Alg != signingAlgorithm) || (j.Use != "" && j.Use != "sig") {
			continue
		}
		pub, err := parseP256(j.X, j.Y)
		if err != nil {
			continue // a bad entry must not hide the good ones
		}
		keys[j.Kid] = pub
	}
	return cacheState{keys: keys, expiresAt: now.Add(cacheTTL(resp.Header.Get("Cache-Control")))}, nil
}

func parseP256(x, y string) (*ecdsa.PublicKey, error) {
	xb, err := base64.RawURLEncoding.DecodeString(x)
	if err != nil || len(xb) != jwkCoordinateSize {
		return nil, errors.New("bad x")
	}
	yb, err := base64.RawURLEncoding.DecodeString(y)
	if err != nil || len(yb) != jwkCoordinateSize {
		return nil, errors.New("bad y")
	}
	point := append([]byte{0x04}, append(xb, yb...)...)
	return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
}

// cacheTTL derives the TTL from Cache-Control: max-age clamped to [minCacheTTL, MaxCacheTTL];
// without a usable max-age, MaxCacheTTL.
func cacheTTL(header string) time.Duration {
	ttl := MaxCacheTTL
	for _, directive := range strings.Split(header, ",") {
		directive = strings.ToLower(strings.TrimSpace(directive))
		if directive == "no-store" || directive == "no-cache" {
			return minCacheTTL
		}
		if v, ok := strings.CutPrefix(directive, "max-age="); ok {
			if secs, err := strconv.Atoi(strings.Trim(v, `"`)); err == nil && secs >= 0 {
				ttl = time.Duration(secs) * time.Second
			}
		}
	}
	return min(max(ttl, minCacheTTL), MaxCacheTTL)
}
