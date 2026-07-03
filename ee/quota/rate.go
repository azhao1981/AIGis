// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package quota

import (
	"sync"
	"time"
)

// RateLimiter caps the number of requests a tenant may make per one-second
// window (QPS). Unlike the concurrency limiter this is a rate, not an in-flight
// count: admitted requests are never "released" — the allowance simply resets
// when the window rolls over. A tenant with no explicit limit falls back to def;
// a limit of 0 (or negative) means unlimited for that tenant. Safe for
// concurrent use.
//
// The window is fixed (aligned to the wall-clock second), which permits up to
// 2x the limit across a window boundary in the worst case — an accepted
// trade-off for a simple, cheap counter.
type RateLimiter struct {
	perTenant map[string]int
	def       int

	mu     sync.Mutex
	window map[string]*rateWindow
	// now is injected for tests; defaults to time.Now.
	now func() time.Time
}

// rateWindow is a tenant's counter for the current one-second window.
type rateWindow struct {
	startSec int64 // unix second the window began
	count    int
}

// NewRateLimiter builds a per-second rate limiter. perTenant maps a tenant to
// its max requests per second; def is the fallback for unlisted tenants (0 =
// unlimited).
func NewRateLimiter(perTenant map[string]int, def int) *RateLimiter {
	pt := make(map[string]int, len(perTenant))
	for k, v := range perTenant {
		pt[k] = v
	}
	return &RateLimiter{
		perTenant: pt,
		def:       def,
		window:    make(map[string]*rateWindow),
		now:       time.Now,
	}
}

// limitFor returns the QPS ceiling for a tenant (explicit override or default).
func (l *RateLimiter) limitFor(tenant string) int {
	if v, ok := l.perTenant[tenant]; ok {
		return v
	}
	return l.def
}

// Allow reports whether the tenant may make one more request in the current
// one-second window, incrementing its counter on admission. Returns true (and
// counts nothing) when the tenant is unlimited.
func (l *RateLimiter) Allow(tenant string) bool {
	max := l.limitFor(tenant)
	if max <= 0 {
		return true // unlimited for this tenant
	}

	sec := l.now().Unix()

	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.window[tenant]
	if w == nil || w.startSec != sec {
		// New or rolled-over window: reset.
		l.window[tenant] = &rateWindow{startSec: sec, count: 1}
		return true
	}
	if w.count >= max {
		return false // window is full
	}
	w.count++
	return true
}
