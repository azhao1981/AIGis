// Package cache provides a small in-memory response cache for the gateway:
// identical (non-streaming) requests within a short TTL get the previous
// response instead of hitting the upstream again. It is disabled by default
// (ttl <= 0) so behavior is unchanged unless configured.
//
// Note: the cached value is the final, unmasked response, and the key is a hash
// of the original request body — so identical requests carry identical PII and
// the cached response stays consistent. The cache therefore holds plaintext in
// memory; keep TTLs short and only enable it for low-sensitivity / deterministic
// workloads.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

type entry struct {
	body     []byte
	expireAt time.Time
}

// TTLCache is an in-memory cache with per-entry TTL and a hard entry cap.
// ttl <= 0 disables it (Get always misses, Set is a no-op). Safe for concurrent use.
type TTLCache struct {
	ttl time.Duration
	max int
	now func() time.Time // injectable clock (tests)

	mu sync.Mutex
	m  map[string]entry
}

// New creates a cache with the given TTL and max entry count (max <= 0 → 1000).
func New(ttl time.Duration, max int) *TTLCache {
	if max <= 0 {
		max = 1000
	}
	return &TTLCache{ttl: ttl, max: max, now: time.Now, m: make(map[string]entry)}
}

func (c *TTLCache) enabled() bool { return c.ttl > 0 }

// Get returns the cached body for key if present and unexpired.
func (c *TTLCache) Get(key string) ([]byte, bool) {
	if !c.enabled() {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.m[key]
	if !ok {
		return nil, false
	}
	if c.now().After(e.expireAt) {
		delete(c.m, key)
		return nil, false
	}
	return e.body, true
}

// Set stores body under key with the configured TTL. When the cache is full it
// first drops expired entries; if still full it skips the write (never grows
// past max). The body is copied so the caller may reuse its slice.
func (c *TTLCache) Set(key string, body []byte) {
	if !c.enabled() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.m[key]; !exists && len(c.m) >= c.max {
		c.evictExpired()
		if len(c.m) >= c.max {
			return // still full of live entries; don't grow unbounded
		}
	}

	b := make([]byte, len(body))
	copy(b, body)
	c.m[key] = entry{body: b, expireAt: c.now().Add(c.ttl)}
}

// evictExpired removes expired entries. Caller holds the lock.
func (c *TTLCache) evictExpired() {
	now := c.now()
	for k, e := range c.m {
		if now.After(e.expireAt) {
			delete(c.m, k)
		}
	}
}

// Key builds a cache key by hashing the given parts (order-sensitive, with a
// separator so parts can't run together).
func Key(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
