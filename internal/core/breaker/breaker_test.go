package breaker

import (
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock for deterministic state transitions.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestBreaker builds a Breaker with an injected clock.
func newTestBreaker(cfg Config, clk *fakeClock) *Breaker {
	return &Breaker{cfg: cfg, now: clk.now, state: StateClosed}
}

func TestBreakerOpensAfterThreshold(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	b := newTestBreaker(Config{Enabled: true, FailThreshold: 3, Cooldown: 30 * time.Second}, clk)

	// Closed: allows, and a success resets the fail counter.
	for i := 0; i < 2; i++ {
		if !b.Allow() {
			t.Fatal("closed breaker should allow")
		}
		b.RecordFailure()
	}
	b.RecordSuccess() // resets consecutive fails to 0
	if b.State() != StateClosed {
		t.Fatalf("state = %v, want closed after success reset", b.State())
	}

	// 3 consecutive failures trip it open.
	for i := 0; i < 3; i++ {
		b.Allow()
		b.RecordFailure()
	}
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want open after threshold", b.State())
	}
	if b.Allow() {
		t.Fatal("open breaker should reject during cooldown")
	}
}

func TestBreakerHalfOpenRecovers(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	b := newTestBreaker(Config{Enabled: true, FailThreshold: 1, Cooldown: 30 * time.Second}, clk)

	b.Allow()
	b.RecordFailure() // threshold 1 → open immediately
	if b.State() != StateOpen {
		t.Fatal("should be open")
	}

	// Still within cooldown: rejected.
	clk.advance(29 * time.Second)
	if b.Allow() {
		t.Fatal("should still reject before cooldown elapses")
	}

	// Cooldown elapsed: first Allow becomes the half-open probe; concurrent ones rejected.
	clk.advance(2 * time.Second)
	if !b.Allow() {
		t.Fatal("should allow one probe after cooldown")
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("state = %v, want half-open", b.State())
	}
	if b.Allow() {
		t.Fatal("second concurrent request must be rejected while probing")
	}

	// Probe succeeds → closed again.
	b.RecordSuccess()
	if b.State() != StateClosed {
		t.Fatalf("state = %v, want closed after successful probe", b.State())
	}
	if !b.Allow() {
		t.Fatal("closed again should allow")
	}
}

func TestBreakerHalfOpenProbeFailReopens(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	b := newTestBreaker(Config{Enabled: true, FailThreshold: 1, Cooldown: 10 * time.Second}, clk)

	b.Allow()
	b.RecordFailure() // open
	clk.advance(10 * time.Second)
	if !b.Allow() {
		t.Fatal("probe should be allowed after cooldown")
	}
	b.RecordFailure() // probe fails → re-open
	if b.State() != StateOpen {
		t.Fatalf("state = %v, want open after probe failure", b.State())
	}
	if b.Allow() {
		t.Fatal("should reject again after re-open")
	}
}

func TestBreakerDisabledIsNoOp(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	b := newTestBreaker(Config{Enabled: false, FailThreshold: 1, Cooldown: time.Second}, clk)
	for i := 0; i < 100; i++ {
		if !b.Allow() {
			t.Fatal("disabled breaker must always allow")
		}
		b.RecordFailure()
	}
	if b.State() != StateClosed {
		t.Fatalf("disabled breaker state = %v, want closed", b.State())
	}
}

func TestSetPerKeyIsolation(t *testing.T) {
	s := NewSet(Config{Enabled: true, FailThreshold: 1, Cooldown: time.Minute})
	a := s.Get("route-a")
	b := s.Get("route-b")

	a.Allow()
	a.RecordFailure() // trip route-a only
	if a.State() != StateOpen {
		t.Fatal("route-a should be open")
	}
	if b.State() != StateClosed || !b.Allow() {
		t.Fatal("route-b must be unaffected by route-a tripping")
	}
	if s.Get("route-a") != a {
		t.Fatal("Get must return the same breaker per key")
	}
}
