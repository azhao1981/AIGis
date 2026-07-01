// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

// Package quota is the Enterprise Edition per-tenant admission limiter. It
// implements the open-source core's quota.Limiter seam (injected via
// server.SetQuotaLimiter), so the dependency is one-way (ee -> core). This
// starter enforces a per-tenant maximum number of concurrently in-flight
// requests; a tenant over its ceiling is rejected (HTTP 429 by the core).
package quota

import (
	"sync"

	corequota "aigis/internal/core/quota"
)

// ConcurrencyLimiter caps concurrently in-flight requests per tenant. A tenant
// with no explicit limit falls back to Default; a limit of 0 (or negative) means
// unlimited for that tenant. Safe for concurrent use.
type ConcurrencyLimiter struct {
	// PerTenant is the explicit per-tenant concurrency ceiling (tenant -> max).
	perTenant map[string]int
	// def is the ceiling applied to tenants absent from perTenant. 0 = unlimited.
	def int

	mu      sync.Mutex
	current map[string]int
}

// NewConcurrencyLimiter builds a limiter. perTenant maps a tenant to its max
// in-flight requests; def is the fallback ceiling for unlisted tenants (0 =
// unlimited).
func NewConcurrencyLimiter(perTenant map[string]int, def int) *ConcurrencyLimiter {
	pt := make(map[string]int, len(perTenant))
	for k, v := range perTenant {
		pt[k] = v
	}
	return &ConcurrencyLimiter{
		perTenant: pt,
		def:       def,
		current:   make(map[string]int),
	}
}

// limitFor returns the ceiling for a tenant (explicit override or the default).
func (l *ConcurrencyLimiter) limitFor(tenant string) int {
	if v, ok := l.perTenant[tenant]; ok {
		return v
	}
	return l.def
}

// Acquire admits a request for tenant unless it is at its concurrency ceiling.
// When admitted it returns a Release that frees the slot; when rejected it
// returns a no-op Release and false.
func (l *ConcurrencyLimiter) Acquire(tenant string) (corequota.Release, bool) {
	max := l.limitFor(tenant)
	if max <= 0 {
		return func() {}, true // unlimited for this tenant
	}

	l.mu.Lock()
	if l.current[tenant] >= max {
		l.mu.Unlock()
		return func() {}, false
	}
	l.current[tenant]++
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			if l.current[tenant] > 0 {
				l.current[tenant]--
			}
			l.mu.Unlock()
		})
	}, true
}
