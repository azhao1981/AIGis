package quota

import (
	"testing"
	"time"
)

// atTime pins the limiter's clock so window rollover is deterministic (no sleep).
func atTime(l *RateLimiter, t time.Time) { l.now = func() time.Time { return t } }

func TestRateLimiter_PerTenantCeiling(t *testing.T) {
	l := NewRateLimiter(map[string]int{"acme": 2}, 0)
	base := time.Unix(1000, 0)
	atTime(l, base)

	if !l.Allow("acme") {
		t.Fatal("1st acme request should be allowed")
	}
	if !l.Allow("acme") {
		t.Fatal("2nd acme request should be allowed (qps 2)")
	}
	if l.Allow("acme") {
		t.Fatal("3rd acme request in same second should be rejected")
	}
}

func TestRateLimiter_WindowResets(t *testing.T) {
	l := NewRateLimiter(map[string]int{"acme": 1}, 0)
	base := time.Unix(2000, 0)
	atTime(l, base)

	if !l.Allow("acme") {
		t.Fatal("1st request should be allowed")
	}
	if l.Allow("acme") {
		t.Fatal("2nd request in same second should be rejected")
	}

	// Advance to the next second: the window resets and admits again.
	atTime(l, base.Add(time.Second))
	if !l.Allow("acme") {
		t.Fatal("request in the next window should be allowed after reset")
	}
}

func TestRateLimiter_DefaultCeiling(t *testing.T) {
	l := NewRateLimiter(nil, 1)
	atTime(l, time.Unix(3000, 0))
	if !l.Allow("unknown") {
		t.Fatal("1st request under default should be allowed")
	}
	if l.Allow("unknown") {
		t.Fatal("2nd request should be rejected (default qps 1)")
	}
}

func TestRateLimiter_Unlimited(t *testing.T) {
	l := NewRateLimiter(nil, 0) // 0 = unlimited
	atTime(l, time.Unix(4000, 0))
	for i := 0; i < 100; i++ {
		if !l.Allow("acme") {
			t.Fatalf("unlimited tenant rejected at iteration %d", i)
		}
	}
}

func TestRateLimiter_PerTenantIsolated(t *testing.T) {
	// One tenant hitting its ceiling must not affect another.
	l := NewRateLimiter(map[string]int{"acme": 1, "globex": 1}, 0)
	atTime(l, time.Unix(5000, 0))
	if !l.Allow("acme") {
		t.Fatal("acme 1st should be allowed")
	}
	if l.Allow("acme") {
		t.Fatal("acme 2nd should be rejected")
	}
	if !l.Allow("globex") {
		t.Fatal("globex must be unaffected by acme's exhausted window")
	}
}
