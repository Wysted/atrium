package forwardauth

import (
	"sync"
	"time"
)

const (
	// DefaultCacheTTL is the lifetime of a cached access decision (contract block 4).
	DefaultCacheTTL = 30 * time.Second
	// maxCacheEntries bounds memory; beyond it entries are not stored (the next request just reads the database).
	maxCacheEntries = 100_000
)

type cacheEntry[V any] struct {
	value   V
	expires time.Time
}

// ttlCache is a TTL cache with a generation counter. Clear bumps the generation, and put discards a value
// whose loader started under an older one: a result computed before an invalidation can never be stored after it.
type ttlCache[K comparable, V any] struct {
	ttl time.Duration
	now func() time.Time

	mu  sync.Mutex
	gen uint64
	m   map[K]cacheEntry[V]
}

func newTTLCache[K comparable, V any](ttl time.Duration, now func() time.Time) *ttlCache[K, V] {
	return &ttlCache[K, V]{ttl: ttl, now: now, m: map[K]cacheEntry[V]{}}
}

// get returns the live value for k and the current generation (to be passed to put after loading).
func (c *ttlCache[K, V]) get(k K) (v V, ok bool, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, found := c.m[k]
	if found && c.now().Before(e.expires) {
		return e.value, true, c.gen
	}
	if found {
		delete(c.m, k)
	}
	return v, false, c.gen
}

func (c *ttlCache[K, V]) put(k K, v V, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen {
		return
	}
	now := c.now()
	if len(c.m) >= maxCacheEntries {
		for key, e := range c.m {
			if !now.Before(e.expires) {
				delete(c.m, key)
			}
		}
		if len(c.m) >= maxCacheEntries {
			return
		}
	}
	c.m[k] = cacheEntry[V]{value: v, expires: now.Add(c.ttl)}
}

func (c *ttlCache[K, V]) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	clear(c.m)
}
