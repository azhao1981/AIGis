// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package quota

import (
	corequota "aigis/internal/core/quota"
)

// RateGate is the QPS check a TenantLimiter consults before the concurrency
// gate. *RateLimiter and *RedisRateLimiter both satisfy it, so the composite
// works with either the in-memory or the distributed rate limiter.
type RateGate interface {
	Allow(tenant string) bool
}

// TenantLimiter composes an optional QPS gate with an optional concurrency gate
// into a single quota.Limiter (the seam the core injects via SetQuotaLimiter).
// It lets a deployment enable QPS, concurrency, or both, independently.
//
// Acquire checks the rate gate FIRST: a request rejected on QPS never reserves a
// concurrency slot (so a throttled tenant can't leak in-flight counters). Only
// after passing the rate gate does it consult the concurrency limiter, whose
// Release is passed straight through — the QPS gate has no slot to free, since a
// rate allowance is reclaimed by the passage of time, not by request completion.
type TenantLimiter struct {
	rate        RateGate          // QPS gate (nil = QPS disabled)
	concurrency corequota.Limiter // concurrency gate (nil = concurrency disabled)
}

// NewTenantLimiter builds a composite limiter. Either gate may be nil to disable
// that dimension. If both are nil the limiter admits everything.
func NewTenantLimiter(rate RateGate, concurrency corequota.Limiter) *TenantLimiter {
	return &TenantLimiter{rate: rate, concurrency: concurrency}
}

// Acquire admits a request for tenant only if it passes the QPS gate (if any)
// and then the concurrency gate (if any). On a QPS rejection it returns a no-op
// Release and false without touching the concurrency counter. On admission it
// returns the concurrency limiter's Release (or a no-op when concurrency is
// disabled).
func (l *TenantLimiter) Acquire(tenant string) (corequota.Release, bool) {
	if l.rate != nil && !l.rate.Allow(tenant) {
		return func() {}, false
	}
	if l.concurrency != nil {
		return l.concurrency.Acquire(tenant)
	}
	return func() {}, true
}
