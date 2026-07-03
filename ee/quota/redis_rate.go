// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package quota

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// ratePrefix namespaces the per-tenant per-second QPS counters in Redis. The
// unix second is folded into the key so each one-second window is a distinct
// counter that expires on its own — no explicit reset needed.
const ratePrefix = "aigis:quota:qps:"

// rateScript atomically counts one request in a tenant's current one-second
// window and reports whether it is within the limit. INCR creates the key at 1
// on the first request of the window; that first request sets a 2-second EXPIRE
// (a hair over the window so a slightly-late clock still lets it lapse). If the
// post-increment count exceeds max it returns -1 (reject); otherwise the count.
// Fixed-window, mirroring the in-memory RateLimiter, done atomically in Lua so
// the count is consistent across replicas.
var rateScript = redis.NewScript(`
local n = redis.call("INCR", KEYS[1])
if n == 1 then
  redis.call("EXPIRE", KEYS[1], 2)
end
local max = tonumber(ARGV[1])
if n > max then
  return -1
end
return n
`)

// RedisRateLimiter enforces a per-tenant QPS ceiling shared across every gateway
// replica via Redis (fixed one-second window). It mirrors the in-memory
// RateLimiter's Allow contract so *TenantLimiter can use either. Like the
// concurrency RedisLimiter it FAILS OPEN when Redis is unreachable: a Redis
// outage must never take the gateway down.
type RedisRateLimiter struct {
	rdb *redis.Client
	log *zap.Logger

	perTenant map[string]int
	def       int
}

// NewRedisRateLimiter builds a distributed QPS limiter from a Redis address,
// verifying connectivity. perTenant maps a tenant to its max requests per
// second; def is the fallback for unlisted tenants (0 = unlimited).
func NewRedisRateLimiter(ctx context.Context, addr, password string, db int, perTenant map[string]int, def int, log *zap.Logger) (*RedisRateLimiter, error) {
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("quota: redis rate ping: %w", err)
	}
	pt := make(map[string]int, len(perTenant))
	for k, v := range perTenant {
		pt[k] = v
	}
	return &RedisRateLimiter{rdb: rdb, log: log, perTenant: pt, def: def}, nil
}

// limitFor returns the QPS ceiling for a tenant (explicit override or default).
func (l *RedisRateLimiter) limitFor(tenant string) int {
	if v, ok := l.perTenant[tenant]; ok {
		return v
	}
	return l.def
}

// Allow reports whether the tenant may make one more request in the current
// one-second window fleet-wide, counting it on admission. Unlimited tenants
// (limit <= 0) are always admitted without touching Redis. On a Redis error it
// fails open (admits).
func (l *RedisRateLimiter) Allow(tenant string) bool {
	max := l.limitFor(tenant)
	if max <= 0 {
		return true // unlimited for this tenant
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	key := fmt.Sprintf("%s%s:%d", ratePrefix, tenant, time.Now().Unix())
	res, err := rateScript.Run(ctx, l.rdb, []string{key}, max).Int64()
	if err != nil {
		// Fail open: never let a Redis outage take the gateway down.
		if l.log != nil {
			l.log.Warn("quota: redis rate check failed, failing open",
				zap.String("tenant", tenant), zap.Error(err))
		}
		return true
	}
	return res >= 0
}

// Close releases the Redis connection.
func (l *RedisRateLimiter) Close() error {
	return l.rdb.Close()
}
