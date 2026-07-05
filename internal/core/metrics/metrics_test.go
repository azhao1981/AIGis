package metrics

import (
	"sync"
	"testing"
)

func TestBeginEnd_Counts(t *testing.T) {
	m := New()
	m.Begin()
	m.Begin()
	if s := m.Snapshot(); s.InFlight != 2 || s.Total != 2 {
		t.Fatalf("after 2 Begin: in_flight=%d total=%d, want 2/2", s.InFlight, s.Total)
	}
	m.End(true)
	m.End(false)
	s := m.Snapshot()
	if s.InFlight != 0 {
		t.Errorf("InFlight = %d, want 0", s.InFlight)
	}
	if s.Total != 2 || s.Success != 1 || s.Failed != 1 {
		t.Errorf("Total/Success/Failed = %d/%d/%d, want 2/1/1", s.Total, s.Success, s.Failed)
	}
	if s.Peak != 2 {
		t.Errorf("Peak = %d, want 2", s.Peak)
	}
}

// TestConcurrent verifies the atomic counters are race-free under load:
// run with -race. All requests complete, in-flight returns to 0, peak is bounded.
func TestConcurrent(t *testing.T) {
	const n = 1000
	m := New()
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			m.Begin()
			m.End(i%2 == 0)
		}(i)
	}
	wg.Wait()

	s := m.Snapshot()
	if s.InFlight != 0 {
		t.Errorf("InFlight = %d, want 0 after all done", s.InFlight)
	}
	if s.Total != n {
		t.Errorf("Total = %d, want %d", s.Total, n)
	}
	if s.Success+s.Failed != n {
		t.Errorf("Success+Failed = %d, want %d", s.Success+s.Failed, n)
	}
	if s.Peak < 1 || s.Peak > n {
		t.Errorf("Peak = %d, want within [1,%d]", s.Peak, n)
	}
}

// TestDimensions_RouteAndPII verifies the per-route / per-rule counters and the
// injection / transform-rejection counters all tally correctly under concurrent
// load (run with -race).
func TestDimensions_RouteAndPII(t *testing.T) {
	m := New()
	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			m.Begin()
			m.RouteMatched("alpha")
			m.PIIHit("Email", 1)
			if i%5 == 0 {
				m.PIIHit("Phone", 2)
				m.InjectionBlocked()
				m.TransformRejected("injection")
			}
			if i%7 == 0 {
				m.TransformRejected("guard")
			}
			m.RouteOutcome("alpha", i%2 == 0)
			m.End(i%2 == 0)
		}(i)
	}
	wg.Wait()

	d := m.Dimensions()
	if got := d.RouteRequests["alpha"]; got != n {
		t.Errorf("alpha requests = %d, want %d", got, n)
	}
	if got := d.PIIHits["Email"]; got != n {
		t.Errorf("Email hits = %d, want %d", got, n)
	}
	// Every 5th iteration (i%5==0): for n=50 that's 10 iterations, each adds 2 Phone hits.
	wantPhone := int64(10 * 2)
	if got := d.PIIHits["Phone"]; got != wantPhone {
		t.Errorf("Phone hits = %d, want %d", got, wantPhone)
	}
	if d.InjectionBlocked != 10 {
		t.Errorf("injection_blocked = %d, want 10", d.InjectionBlocked)
	}
	if got := d.TransformRejects["injection"]; got != 10 {
		t.Errorf("injection reject = %d, want 10", got)
	}
	if got := d.TransformRejects["guard"]; got != 8 { // i%7==0 within [0,50): 0,7,14,21,28,35,42,49 = 8
		t.Errorf("guard reject = %d, want 8", got)
	}
	// alpha failed = number of i%2!=0 = 25
	if got := d.RouteFailed["alpha"]; got != 25 {
		t.Errorf("alpha failed = %d, want 25", got)
	}
}

// TestDimensions_LatencyHistogram verifies bucket placement is monotonic and
// the cumulative count matches the total observations.
func TestDimensions_LatencyHistogram(t *testing.T) {
	m := New()
	// Buckets: 10,50,100,250,500,1000,2500,5000,10000, +Inf
	m.ObserveLatency(5 * 1e6)   // 5s   -> 5000 bucket (index 7)
	m.ObserveLatency(20)        // 20us -> 0.02ms -> 50 bucket (index 1)
	m.ObserveLatency(2 * 1e9)   // 2s   -> 2000ms -> 2500 bucket (index 6)
	d := m.Dimensions()
	if d.HistCount != 3 {
		t.Fatalf("count = %d, want 3", d.HistCount)
	}
	// Cumulative buckets must be non-decreasing and the last must equal total count.
	var prev int64
	for i, c := range d.HistCounts {
		if c < prev {
			t.Errorf("bucket %d (le=%v) count %d < previous %d (not cumulative)", i, d.HistBuckets[i], c, prev)
		}
		prev = c
	}
	if d.HistCounts[len(d.HistCounts)-1] != 3 {
		t.Errorf("top bucket cumulative = %d, want 3", d.HistCounts[len(d.HistCounts)-1])
	}
}
