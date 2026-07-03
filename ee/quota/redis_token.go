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

// tokenPrefix namespaces the per-tenant per-period token counters in Redis. The
// period-start unix second is folded into the key so each period is a distinct
// counter that expires on its own once the period ends — no explicit reset.
const tokenPrefix = "aigis:quota:tokens:"

// tokenAddScript atomically books tokens into a tenant's current period counter
// and sets the period TTL on the first write. INCRBY creates the key at the
// added amount on the first booking; that first booking sets EXPIRE to the
// seconds remaining until the period ends so the counter lapses on roll-over.
// Fixed-window, mirroring the in-memory TokenLimiter, done atomically in Lua so
// the tally is consistent across replicas.
var tokenAddScript = redis.NewScript(`
local n = redis.call("INCRBY", KEYS[1], ARGV[1])
local ttl = tonumber(ARGV[2])
if n == tonumber(ARGV[1]) and ttl > 0 then
  redis.call("EXPIRE", KEYS[1], ttl)
end
return n
`)

// RedisTokenLimiter enforces a per-tenant token ceiling per fixed period shared
// across every gateway replica via Redis. It mirrors the in-memory
// TokenLimiter's TokenGate contract (Allow + Add) so *TenantLimiter can use
// either. Like the other Redis limiters it FAILS OPEN when Redis is
// unreachable: a Redis outage must never take the gateway down — Allow admits,
// Add silently drops.
type RedisTokenLimiter struct {
	rdb    *redis.Client
	log    *zap.Logger
	period Period

	perTenant map[string]int
	def       int
}

// NewRedisTokenLimiter builds a distributed token limiter from a Redis address,
// verifying connectivity. perTenant maps a tenant to its max tokens per period;
// def is the fallback for unlisted tenants (0 = unlimited).
func NewRedisTokenLimiter(ctx context.Context, addr, password string, db int, perTenant map[string]int, def int, period Period, log *zap.Logger) (*RedisTokenLimiter, error) {
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("quota: redis token ping: %w", err)
	}
	pt := make(map[string]int, len(perTenant))
	for k, v := range perTenant {
		pt[k] = v
	}
	return &RedisTokenLimiter{rdb: rdb, log: log, period: period, perTenant: pt, def: def}, nil
}

// limitFor returns the token ceiling for a tenant (explicit override or default).
func (l *RedisTokenLimiter) limitFor(tenant string) int {
	if v, ok := l.perTenant[tenant]; ok {
		return v
	}
	return l.def
}

// Allow reports whether the tenant is still under its token ceiling for the
// current period fleet-wide (a read of the period-to-date total, no mutation).
// Unlimited tenants (limit <= 0) are admitted without touching Redis. On a
// Redis error it fails open (admits).
func (l *RedisTokenLimiter) Allow(tenant string) bool {
	max := l.limitFor(tenant)
	if max <= 0 {
		return true // unlimited for this tenant
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start, _ := periodBounds(l.period, time.Now())
	key := fmt.Sprintf("%s%s:%d", tokenPrefix, tenant, start)
	used, err := l.rdb.Get(ctx, key).Int64()
	if err == redis.Nil {
		return true // nothing spent yet this period
	}
	if err != nil {
		if l.log != nil {
			l.log.Warn("quota: redis token check failed, failing open",
				zap.String("tenant", tenant), zap.Error(err))
		}
		return true
	}
	return used < int64(max)
}

// Add books the tokens a completed request spent against the tenant's current
// period, fleet-wide. No-op for unlimited tenants or non-positive counts. On a
// Redis error it silently drops the booking (fail open).
func (l *RedisTokenLimiter) Add(tenant string, tokens int) {
	if tokens <= 0 {
		return
	}
	if l.limitFor(tenant) <= 0 {
		return // unlimited: not worth counting
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	now := time.Now()
	start, end := periodBounds(l.period, now)
	ttl := end - now.Unix()
	if ttl < 1 {
		ttl = 1
	}
	key := fmt.Sprintf("%s%s:%d", tokenPrefix, tenant, start)
	if err := tokenAddScript.Run(ctx, l.rdb, []string{key}, tokens, ttl).Err(); err != nil {
		if l.log != nil {
			l.log.Warn("quota: redis token add failed, dropping booking",
				zap.String("tenant", tenant), zap.Int("tokens", tokens), zap.Error(err))
		}
	}
}

// Close releases the Redis connection.
func (l *RedisTokenLimiter) Close() error {
	return l.rdb.Close()
}
