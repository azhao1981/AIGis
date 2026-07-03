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

// TenantLimiter composes an optional QPS gate, an optional token-budget gate,
// and an optional concurrency gate into a single quota.Limiter (the seam the
// core injects via SetQuotaLimiter). It lets a deployment enable any subset of
// the three dimensions independently.
//
// Acquire checks the gates in order QPS -> token -> concurrency, cheapest and
// most-likely-to-reject first, and short-circuits: a request rejected on QPS or
// token budget never reserves a concurrency slot (so a throttled tenant can't
// leak in-flight counters). The token gate is an event-time check only — it
// reads the period-to-date total but does NOT book the request's tokens here,
// since they are unknown until the upstream replies; TokenMeteringSink books
// them (via TokenGate.Add) after the response. Concurrency's Release is passed
// straight through — neither the QPS nor token gate has a slot to free.
type TenantLimiter struct {
	rate        RateGate          // QPS gate (nil = QPS disabled)
	token       TokenGate         // token-budget gate (nil = token quota disabled)
	concurrency corequota.Limiter // concurrency gate (nil = concurrency disabled)
}

// NewTenantLimiter builds a composite limiter. Any gate may be nil to disable
// that dimension. If all are nil the limiter admits everything.
func NewTenantLimiter(rate RateGate, token TokenGate, concurrency corequota.Limiter) *TenantLimiter {
	return &TenantLimiter{rate: rate, token: token, concurrency: concurrency}
}

// Acquire admits a request for tenant only if it passes the QPS gate (if any),
// then the token-budget gate (if any), then the concurrency gate (if any). On a
// QPS or token rejection it returns a no-op Release and false without touching
// the concurrency counter. On admission it returns the concurrency limiter's
// Release (or a no-op when concurrency is disabled).
func (l *TenantLimiter) Acquire(tenant string) (corequota.Release, bool) {
	if l.rate != nil && !l.rate.Allow(tenant) {
		return func() {}, false
	}
	if l.token != nil && !l.token.Allow(tenant) {
		return func() {}, false
	}
	if l.concurrency != nil {
		return l.concurrency.Acquire(tenant)
	}
	return func() {}, true
}
