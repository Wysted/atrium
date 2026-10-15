// Package ratelimit is a small in-memory token-bucket limiter keyed by string (client IP).
// State lives in the process: with several replicas the effective limit is per replica.
package ratelimit

import (
	"net/netip"
	"sync"
	"time"
)

const (
	// DefaultBurst is how many requests a key may make at once.
	DefaultBurst = 30
	// DefaultRefillEvery is the time to earn one request back (30 requests per minute sustained).
	DefaultRefillEvery = 2 * time.Second
	// DefaultMaxKeys bounds memory; beyond it, new keys are refused until idle ones age out (fail closed).
	DefaultMaxKeys = 50_000
	// DefaultSweepEvery is how often idle keys are dropped. The sweep is O(keys), so it runs on a timer and
	// never per request: a flood of new keys at the cap costs O(1) each.
	DefaultSweepEvery = 30 * time.Second

	// IPv6PrefixBits is the granularity of IPv6 clients: a single subscriber usually owns a whole /64 and can
	// rotate addresses inside it for free, so the /64 is the unit. IPv4 clients are keyed by their address.
	IPv6PrefixBits = 64
)

// ClientKey maps a client address to its rate-limit key: the address itself for IPv4 (/32), the /64 prefix for
// IPv6, and "unknown" for an invalid address.
func ClientKey(ip netip.Addr) string {
	if !ip.IsValid() {
		return "unknown"
	}
	ip = ip.Unmap()
	if ip.Is4() {
		return ip.String()
	}
	return netip.PrefixFrom(ip, IPv6PrefixBits).Masked().String()
}

// Config tunes a Limiter; zero values take the defaults. Now is injectable for tests.
type Config struct {
	Burst       int
	RefillEvery time.Duration
	MaxKeys     int
	SweepEvery  time.Duration
	Now         func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// Limiter is safe for concurrent use.
type Limiter struct {
	burst      float64
	refill     time.Duration
	maxKeys    int
	sweepEvery time.Duration
	now        func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
	sweeps    int // number of sweeps run (tests)
}

// New builds a Limiter.
func New(cfg Config) *Limiter {
	l := &Limiter{burst: float64(cfg.Burst), refill: cfg.RefillEvery, maxKeys: cfg.MaxKeys, sweepEvery: cfg.SweepEvery, now: cfg.Now, buckets: map[string]*bucket{}}
	if cfg.Burst <= 0 {
		l.burst = DefaultBurst
	}
	if l.refill <= 0 {
		l.refill = DefaultRefillEvery
	}
	if l.maxKeys <= 0 {
		l.maxKeys = DefaultMaxKeys
	}
	if l.sweepEvery <= 0 {
		l.sweepEvery = DefaultSweepEvery
	}
	if l.now == nil {
		l.now = time.Now
	}
	l.lastSweep = l.now()
	return l
}

// Allow consumes one token of key. When refused it returns how long until a token is available.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= l.sweepEvery {
		l.sweep(now)
	}
	b, found := l.buckets[key]
	if !found {
		if len(l.buckets) >= l.maxKeys { // full: refuse until the next scheduled sweep frees idle keys
			return false, l.refill
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+float64(now.Sub(b.last))/float64(l.refill))
	b.last = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) * float64(l.refill))
	}
	b.tokens--
	return true, 0
}

// sweep drops keys that have fully refilled: forgetting them changes nothing.
func (l *Limiter) sweep(now time.Time) {
	l.lastSweep = now
	l.sweeps++
	for k, b := range l.buckets {
		if b.tokens+float64(now.Sub(b.last))/float64(l.refill) >= l.burst {
			delete(l.buckets, k)
		}
	}
}
