// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package quota

import (
	"strings"
	"sync"
	"time"
)

// TokenGate is the token-quota seam the composite limiter consumes. Allow is
// checked at request-admission time (event-time gate); Add is called after the
// response, from the usage sink, to accumulate the tokens the request actually
// spent. Keeping the two apart is deliberate: token counts are unknown until
// the upstream replies, so admission cannot pre-reserve — a request is only
// rejected once the period-to-date total has already crossed the ceiling.
type TokenGate interface {
	Allow(tenant string) bool
	Add(tenant string, tokens int)
}

// TokenLimiter caps the cumulative tokens a tenant may spend per fixed period
// (day / hour / month, aligned to the period start in UTC). It is a running
// counter, not an in-flight count: admitted requests are never "released" — the
// tally simply resets when the period rolls over. A tenant with no explicit
// limit falls back to def; a limit of 0 (or negative) means unlimited. Safe for
// concurrent use.
//
// Because the ceiling is enforced on the period-to-date total (checked before a
// request, incremented after it), a single request may push a tenant slightly
// past its limit — the overshoot is bounded by one request's token count and is
// an accepted trade-off for a cheap counter.
type TokenLimiter struct {
	perTenant map[string]int
	def       int
	period    Period

	mu     sync.Mutex
	window map[string]*tokenWindow
	// now is injected for tests; defaults to time.Now.
	now func() time.Time
}

// tokenWindow is a tenant's token tally for the current period.
type tokenWindow struct {
	startUnix int64 // unix second the period began
	used      int
}

// Period is the reset granularity of a token quota.
type Period int

const (
	// PeriodDay resets at 00:00:00 UTC each day (the default).
	PeriodDay Period = iota
	// PeriodHour resets at the top of each UTC hour.
	PeriodHour
	// PeriodMonth resets at the first instant of each UTC calendar month.
	PeriodMonth
)

// ParsePeriod maps a config string to a Period, defaulting to PeriodDay for
// empty or unrecognized input.
func ParsePeriod(s string) Period {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "hour":
		return PeriodHour
	case "month":
		return PeriodMonth
	default:
		return PeriodDay
	}
}

// NewTokenLimiter builds a per-period token limiter. perTenant maps a tenant to
// its max tokens per period; def is the fallback for unlisted tenants (0 =
// unlimited).
func NewTokenLimiter(perTenant map[string]int, def int, period Period) *TokenLimiter {
	pt := make(map[string]int, len(perTenant))
	for k, v := range perTenant {
		pt[k] = v
	}
	return &TokenLimiter{
		perTenant: pt,
		def:       def,
		period:    period,
		window:    make(map[string]*tokenWindow),
		now:       time.Now,
	}
}

// limitFor returns the token ceiling for a tenant (explicit override or default).
func (l *TokenLimiter) limitFor(tenant string) int {
	if v, ok := l.perTenant[tenant]; ok {
		return v
	}
	return l.def
}

// periodBounds returns the unix second at which the period containing t began
// and the second at which it ends, in UTC. Shared by the in-memory and Redis
// limiters so both agree on window alignment (and the Redis TTL matches the
// window).
func periodBounds(p Period, t time.Time) (start, end int64) {
	u := t.UTC()
	switch p {
	case PeriodHour:
		s := time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), 0, 0, 0, time.UTC)
		return s.Unix(), s.Add(time.Hour).Unix()
	case PeriodMonth:
		s := time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
		return s.Unix(), s.AddDate(0, 1, 0).Unix()
	default: // PeriodDay
		s := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
		return s.Unix(), s.AddDate(0, 0, 1).Unix()
	}
}

// periodStart returns the unix second at which the current period began for t.
func (l *TokenLimiter) periodStart(t time.Time) int64 {
	start, _ := periodBounds(l.period, t)
	return start
}

// tallyLocked returns the tenant's live window for the current period, resetting
// (or creating) it on a period roll-over. Caller must hold l.mu.
func (l *TokenLimiter) tallyLocked(tenant string, start int64) *tokenWindow {
	w := l.window[tenant]
	if w == nil || w.startUnix != start {
		w = &tokenWindow{startUnix: start, used: 0}
		l.window[tenant] = w
	}
	return w
}

// Allow reports whether the tenant is still under its token ceiling for the
// current period. It does not increment anything — tokens are booked by Add
// after the response. Returns true when the tenant is unlimited.
func (l *TokenLimiter) Allow(tenant string) bool {
	max := l.limitFor(tenant)
	if max <= 0 {
		return true // unlimited for this tenant
	}

	start := l.periodStart(l.now())

	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.tallyLocked(tenant, start)
	return w.used < max
}

// Add books the tokens a completed request spent against the tenant's current
// period. No-op for unlimited tenants or non-positive counts.
func (l *TokenLimiter) Add(tenant string, tokens int) {
	if tokens <= 0 {
		return
	}
	if l.limitFor(tenant) <= 0 {
		return // unlimited: not worth counting
	}

	start := l.periodStart(l.now())

	l.mu.Lock()
	defer l.mu.Unlock()

	w := l.tallyLocked(tenant, start)
	w.used += tokens
}
