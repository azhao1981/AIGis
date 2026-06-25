// Package metrics provides lightweight, lock-free concurrency monitoring for the
// gateway: how many requests are in flight right now, the high-water mark, and
// cumulative totals. It is monitoring only — it never rejects or limits traffic.
package metrics

import (
	"sync/atomic"
	"time"
)

// Metrics tracks request concurrency with atomic counters (safe for concurrent use).
type Metrics struct {
	inFlight  int64
	peak      int64 // high-water mark of inFlight
	total     int64
	success   int64
	failed    int64
	startUnix int64
}

// New returns a Metrics with the uptime clock started.
func New() *Metrics {
	return &Metrics{startUnix: time.Now().Unix()}
}

// Begin marks a request as started: bumps in-flight + total and updates the peak.
// Pair every Begin with exactly one End (use defer).
func (m *Metrics) Begin() {
	cur := atomic.AddInt64(&m.inFlight, 1)
	atomic.AddInt64(&m.total, 1)

	// Raise the high-water mark if this request set a new peak (CAS retry loop).
	for {
		p := atomic.LoadInt64(&m.peak)
		if cur <= p || atomic.CompareAndSwapInt64(&m.peak, p, cur) {
			break
		}
	}
}

// End marks a request as finished, decrementing in-flight and tallying the outcome.
func (m *Metrics) End(ok bool) {
	atomic.AddInt64(&m.inFlight, -1)
	if ok {
		atomic.AddInt64(&m.success, 1)
	} else {
		atomic.AddInt64(&m.failed, 1)
	}
}

// Snapshot is a point-in-time view of the counters, suitable for JSON output.
type Snapshot struct {
	InFlight  int64 `json:"in_flight"`
	Peak      int64 `json:"peak_concurrency"`
	Total     int64 `json:"total_requests"`
	Success   int64 `json:"success"`
	Failed    int64 `json:"failed"`
	UptimeSec int64 `json:"uptime_sec"`
}

// Snapshot atomically reads the current counters.
func (m *Metrics) Snapshot() Snapshot {
	return Snapshot{
		InFlight:  atomic.LoadInt64(&m.inFlight),
		Peak:      atomic.LoadInt64(&m.peak),
		Total:     atomic.LoadInt64(&m.total),
		Success:   atomic.LoadInt64(&m.success),
		Failed:    atomic.LoadInt64(&m.failed),
		UptimeSec: time.Now().Unix() - m.startUnix,
	}
}
