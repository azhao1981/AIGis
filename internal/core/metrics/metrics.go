// Package metrics provides lightweight, lock-free concurrency monitoring for the
// gateway: how many requests are in flight right now, the high-water mark, and
// cumulative totals — plus per-route counters, a coarse latency histogram, and
// per-rule PII / injection hit counters (the dimensions that map to AIGis's
// egress-protection value). It is monitoring only — it never rejects or limits
// traffic.
package metrics

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics tracks request concurrency with atomic counters (safe for concurrent use).
// The global counters are lock-free; the per-route / per-rule dimensions live
// behind a small mutex (write frequency = RPS, label cardinality = config size).
type Metrics struct {
	inFlight  int64
	peak      int64 // high-water mark of inFlight
	total     int64
	success   int64
	failed    int64
	startUnix int64

	mu               sync.Mutex
	routeReqs        map[string]int64 // route_id -> total requests
	routeFailed      map[string]int64 // route_id -> failed requests
	piiHits          map[string]int64 // rule name -> times masked
	injBlocks        int64            // requests rejected by injection transform
	transformRejects map[string]int64 // transform type -> client-rejected count (injection/guard/pii)

	// Latency histogram (coarse, fixed buckets in milliseconds).
	histBuckets []float64 // upper bounds (ms), sorted
	histCounts  []int64   // count per bucket (last bucket is +Inf)
	histSum     float64   // total latency in ms (for mean)
	histCount   int64     // total observations
}

// New returns a Metrics with the uptime clock started.
func New() *Metrics {
	return &Metrics{
		startUnix:        time.Now().Unix(),
		routeReqs:        make(map[string]int64),
		routeFailed:      make(map[string]int64),
		piiHits:          make(map[string]int64),
		transformRejects: make(map[string]int64),
		histBuckets:      []float64{10, 50, 100, 250, 500, 1000, 2500, 5000, 10000},
		histCounts:       make([]int64, 10), // 9 finite buckets + 1 +Inf
	}
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

// RouteMatched records that a request was routed to routeID. Call once per
// request, after the route is resolved.
func (m *Metrics) RouteMatched(routeID string) {
	if routeID == "" {
		return
	}
	m.mu.Lock()
	m.routeReqs[routeID]++
	m.mu.Unlock()
}

// RouteOutcome records the per-route success/failure tally. Pair with RouteMatched.
func (m *Metrics) RouteOutcome(routeID string, ok bool) {
	if routeID == "" || ok {
		return
	}
	m.mu.Lock()
	m.routeFailed[routeID]++
	m.mu.Unlock()
}

// PIIHit increments the per-rule sensitive-info masking counter by n (>= 1).
// Empty rule names are dropped to keep the cardinality meaningful.
func (m *Metrics) PIIHit(rule string, n int) {
	if rule == "" || n <= 0 {
		return
	}
	m.mu.Lock()
	m.piiHits[rule] += int64(n)
	m.mu.Unlock()
}

// InjectionBlocked records that an injection transform rejected a request.
func (m *Metrics) InjectionBlocked() {
	atomic.AddInt64(&m.injBlocks, 1)
}

// TransformRejected records a per-transform client-side rejection (injection,
// guard budget, pii leak). transformType is the strategy name, e.g. "injection".
func (m *Metrics) TransformRejected(transformType string) {
	if transformType == "" {
		return
	}
	m.mu.Lock()
	m.transformRejects[transformType]++
	m.mu.Unlock()
}

// ObserveLatency records a request's end-to-end latency into the histogram.
func (m *Metrics) ObserveLatency(d time.Duration) {
	ms := float64(d.Microseconds()) / 1000.0
	m.mu.Lock()
	for i, upper := range m.histBuckets {
		if ms <= upper {
			m.histCounts[i]++
			goto done
		}
	}
	m.histCounts[len(m.histCounts)-1]++ // +Inf bucket
done:
	m.histSum += ms
	m.histCount++
	m.mu.Unlock()
}

// Snapshot is a point-in-time view of the global counters, suitable for JSON output.
type Snapshot struct {
	InFlight  int64 `json:"in_flight"`
	Peak      int64 `json:"peak_concurrency"`
	Total     int64 `json:"total_requests"`
	Success   int64 `json:"success"`
	Failed    int64 `json:"failed"`
	UptimeSec int64 `json:"uptime_sec"`
}

// Snapshot atomically reads the global counters.
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

// DimensionSnapshot is the per-route / per-rule view for Prometheus output.
// Maps are returned as sorted key/count slices for deterministic exposition.
type DimensionSnapshot struct {
	RouteRequests    map[string]int64
	RouteFailed      map[string]int64
	PIIHits          map[string]int64
	InjectionBlocked int64
	TransformRejects map[string]int64
	HistBuckets      []float64 // upper bounds (ms)
	HistCounts       []int64   // cumulative count at each bucket upper bound
	HistInfCount     int64     // observations in the +Inf bucket
	HistSumMs        float64
	HistCount        int64
}

// Dimensions returns a copy of the dimension counters (sorted maps). The
// histogram bucket counts are cumulative (Prometheus convention) and include
// a synthetic 0 lower bound + +Inf tail.
func (m *Metrics) Dimensions() DimensionSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	snap := DimensionSnapshot{
		RouteRequests:    copyMap(m.routeReqs),
		RouteFailed:      copyMap(m.routeFailed),
		PIIHits:          copyMap(m.piiHits),
		InjectionBlocked: atomic.LoadInt64(&m.injBlocks),
		TransformRejects: copyMap(m.transformRejects),
		HistBuckets:      append([]float64(nil), m.histBuckets...),
		HistCounts:       make([]int64, len(m.histBuckets)),
		HistInfCount:     m.histCounts[len(m.histCounts)-1],
		HistSumMs:        m.histSum,
		HistCount:        m.histCount,
	}
	// Make bucket counts cumulative (each bucket = count of observations <= its upper bound).
	var cum int64
	for i, upper := range m.histBuckets {
		cum += m.histCounts[i]
		snap.HistCounts[i] = cum
		_ = upper
	}
	return snap
}

func copyMap(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// SortedKeys is a tiny helper for deterministic map iteration in tests / debug.
func SortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
