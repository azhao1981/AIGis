package limiter

import (
	"sync"
	"testing"
)

// TestConcurrencyLimiterAdmission: up to max Acquire succeed, the next is
// rejected, and a Release frees exactly one slot.
func TestConcurrencyLimiterAdmission(t *testing.T) {
	l := New(2)

	if !l.Acquire() {
		t.Fatal("1st Acquire should succeed")
	}
	if !l.Acquire() {
		t.Fatal("2nd Acquire should succeed")
	}
	if l.Acquire() {
		t.Fatal("third Acquire should be rejected at max=2")
	}
	if l.InFlight() != 2 {
		t.Fatalf("InFlight = %d, want 2", l.InFlight())
	}

	l.Release()
	if !l.Acquire() {
		t.Fatal("Acquire should succeed after a Release frees a slot")
	}
	l.Release()
	l.Release()
	if l.InFlight() != 0 {
		t.Fatalf("InFlight = %d, want 0 after all released", l.InFlight())
	}
}

// TestConcurrencyLimiterDisabled: max<=0 always admits and never tracks slots.
func TestConcurrencyLimiterDisabled(t *testing.T) {
	for _, max := range []int{0, -1} {
		l := New(max)
		for i := 0; i < 1000; i++ {
			if !l.Acquire() {
				t.Fatalf("max=%d should always admit", max)
			}
		}
		if l.InFlight() != 0 {
			t.Errorf("disabled limiter should not track slots, got %d", l.InFlight())
		}
	}
}

// TestConcurrencyLimiterRace hammers Acquire/Release concurrently and asserts
// the cap is never exceeded and slots return to zero. Run with -race.
func TestConcurrencyLimiterRace(t *testing.T) {
	const max = 8
	l := New(max)

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if l.Acquire() {
					if n := l.InFlight(); n > max {
						t.Errorf("InFlight %d exceeded max %d", n, max)
					}
					l.Release()
				}
			}
		}()
	}
	wg.Wait()

	if l.InFlight() != 0 {
		t.Errorf("InFlight = %d, want 0 after all goroutines done", l.InFlight())
	}
}
