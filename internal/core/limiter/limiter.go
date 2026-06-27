// Package limiter provides admission control for the gateway. Unlike the metrics
// package (monitoring only), the limiter actively rejects traffic that would
// exceed a configured ceiling, so a burst can't exhaust upstream/connection
// resources.
package limiter

import "sync/atomic"

// ConcurrencyLimiter caps the number of simultaneously in-flight requests.
// A max of 0 (or negative) means unlimited — the limiter becomes a no-op, so the
// feature is disabled by default with zero overhead beyond an atomic load skip.
type ConcurrencyLimiter struct {
	max     int64
	current int64
}

// New returns a ConcurrencyLimiter allowing at most max concurrent slots.
// max <= 0 disables limiting.
func New(max int) *ConcurrencyLimiter {
	return &ConcurrencyLimiter{max: int64(max)}
}

// Acquire tries to reserve a slot. It returns true when admitted (the caller
// MUST pair it with exactly one Release, e.g. via defer) and false when the
// limit is already reached (the caller should reject, e.g. HTTP 429, and MUST
// NOT call Release). When limiting is disabled it always returns true.
func (l *ConcurrencyLimiter) Acquire() bool {
	if l.max <= 0 {
		return true // unlimited
	}
	// CAS loop so current is only ever incremented while strictly below max —
	// it never transiently overshoots the ceiling (unlike add-then-rollback).
	for {
		cur := atomic.LoadInt64(&l.current)
		if cur >= l.max {
			return false
		}
		if atomic.CompareAndSwapInt64(&l.current, cur, cur+1) {
			return true
		}
		// lost the race; retry with the fresh value
	}
}

// Release frees a slot previously reserved by a successful Acquire. It is a
// no-op when limiting is disabled.
func (l *ConcurrencyLimiter) Release() {
	if l.max <= 0 {
		return
	}
	atomic.AddInt64(&l.current, -1)
}

// InFlight returns the number of currently held slots (0 when disabled).
func (l *ConcurrencyLimiter) InFlight() int64 {
	return atomic.LoadInt64(&l.current)
}
