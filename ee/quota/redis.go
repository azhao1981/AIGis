// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package quota

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	corequota "aigis/internal/core/quota"
)

// keyPrefix namespaces the per-tenant concurrency counters in Redis.
const keyPrefix = "aigis:quota:concurrency:"

// slotTTL is a safety net: if a replica crashes without releasing a slot, the
// counter is refreshed on every acquire and expires after this idle window, so
// leaked slots self-heal rather than permanently starving a tenant.
const slotTTL = 5 * time.Minute

// acquireScript atomically admits one in-flight request for a tenant: it reads
// the current counter, rejects (returns -1) if already at the ceiling, otherwise
// increments, refreshes the TTL, and returns the new count. Doing this in a Lua
// script makes the check-and-increment a single atomic operation across all
// replicas — the whole point of the distributed limiter.
var acquireScript = redis.NewScript(`
local current = tonumber(redis.call("GET", KEYS[1]) or "0")
local max = tonumber(ARGV[1])
if current >= max then
  return -1
end
local n = redis.call("INCR", KEYS[1])
redis.call("PEXPIRE", KEYS[1], ARGV[2])
return n
`)

// RedisLimiter enforces a per-tenant in-flight ceiling shared across every
// gateway replica via Redis, so a tenant's concurrency is bounded fleet-wide
// rather than per-process. It implements the same Limiter seam as the in-memory
// ConcurrencyLimiter, so it is a drop-in replacement.
//
// If Redis is unreachable at acquire time it FAILS OPEN (admits the request):
// availability of the gateway is prioritized over strict quota enforcement, and
// a warning is logged. This mirrors the metering sink's "never degrade the
// gateway" stance.
type RedisLimiter struct {
	rdb *redis.Client
	log *zap.Logger

	perTenant map[string]int
	def       int
}

// NewRedisLimiter builds a distributed limiter from a Redis address (host:port),
// verifying connectivity. perTenant maps a tenant to its max in-flight requests;
// def is the fallback ceiling for unlisted tenants (0 = unlimited).
func NewRedisLimiter(ctx context.Context, addr, password string, db int, perTenant map[string]int, def int, log *zap.Logger) (*RedisLimiter, error) {
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("quota: redis ping: %w", err)
	}
	pt := make(map[string]int, len(perTenant))
	for k, v := range perTenant {
		pt[k] = v
	}
	return &RedisLimiter{rdb: rdb, log: log, perTenant: pt, def: def}, nil
}

// limitFor returns the ceiling for a tenant (explicit override or the default).
func (l *RedisLimiter) limitFor(tenant string) int {
	if v, ok := l.perTenant[tenant]; ok {
		return v
	}
	return l.def
}

// Acquire admits a request for tenant unless the fleet-wide in-flight count is
// at its ceiling. On admission it returns a Release that decrements the shared
// counter; on rejection it returns a no-op Release and false.
func (l *RedisLimiter) Acquire(tenant string) (corequota.Release, bool) {
	max := l.limitFor(tenant)
	if max <= 0 {
		return func() {}, true // unlimited for this tenant
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	key := keyPrefix + tenant
	res, err := acquireScript.Run(ctx, l.rdb, []string{key}, max, slotTTL.Milliseconds()).Int64()
	if err != nil {
		// Fail open: never let a Redis outage take the gateway down.
		if l.log != nil {
			l.log.Warn("quota: redis acquire failed, failing open",
				zap.String("tenant", tenant), zap.Error(err))
		}
		return func() {}, true
	}
	if res < 0 {
		return func() {}, false // at ceiling
	}

	var once sync.Once
	return func() {
		once.Do(func() { l.release(key) })
	}, true
}

// release decrements the shared counter, guarding against going below zero.
func (l *RedisLimiter) release(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	// DECR then floor at 0 if a race/leak pushed it negative.
	if n, err := l.rdb.Decr(ctx, key).Result(); err == nil && n < 0 {
		l.rdb.Set(ctx, key, 0, slotTTL)
	}
}

// Close releases the Redis connection.
func (l *RedisLimiter) Close() error {
	return l.rdb.Close()
}
