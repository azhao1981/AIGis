package quota

import "testing"

func TestConcurrencyLimiter_PerTenantCeiling(t *testing.T) {
	l := NewConcurrencyLimiter(map[string]int{"acme": 2}, 0)

	r1, ok := l.Acquire("acme")
	if !ok {
		t.Fatal("1st acme acquire should succeed")
	}
	r2, ok := l.Acquire("acme")
	if !ok {
		t.Fatal("2nd acme acquire should succeed (ceiling 2)")
	}
	if _, ok := l.Acquire("acme"); ok {
		t.Fatal("3rd acme acquire should be rejected (over ceiling)")
	}

	// Releasing one slot lets a new request in.
	r1()
	r3, ok := l.Acquire("acme")
	if !ok {
		t.Fatal("acquire after release should succeed")
	}
	r2()
	r3()
}

func TestConcurrencyLimiter_DefaultCeiling(t *testing.T) {
	l := NewConcurrencyLimiter(nil, 1)
	r1, ok := l.Acquire("unknown")
	if !ok {
		t.Fatal("1st acquire under default should succeed")
	}
	if _, ok := l.Acquire("unknown"); ok {
		t.Fatal("2nd acquire should be rejected (default ceiling 1)")
	}
	r1()
}

func TestConcurrencyLimiter_Unlimited(t *testing.T) {
	l := NewConcurrencyLimiter(nil, 0) // 0 = unlimited
	for i := 0; i < 100; i++ {
		if _, ok := l.Acquire("acme"); !ok {
			t.Fatalf("unlimited tenant rejected at iteration %d", i)
		}
	}
}

func TestConcurrencyLimiter_ReleaseIdempotent(t *testing.T) {
	l := NewConcurrencyLimiter(map[string]int{"acme": 1}, 0)
	r, _ := l.Acquire("acme")
	r()
	r() // double-release must not free a slot twice
	if l.current["acme"] != 0 {
		t.Fatalf("current[acme] = %d, want 0 after idempotent release", l.current["acme"])
	}
	// A fresh acquire still works and respects the ceiling of 1.
	if _, ok := l.Acquire("acme"); !ok {
		t.Fatal("acquire after idempotent release should succeed")
	}
	if _, ok := l.Acquire("acme"); ok {
		t.Fatal("double-release must not have inflated the slot count")
	}
}
