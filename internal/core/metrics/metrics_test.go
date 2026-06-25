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
