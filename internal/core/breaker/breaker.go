// Package breaker provides a per-route circuit breaker: when an upstream starts
// failing, the breaker "opens" and fails fast for a cooldown instead of piling
// requests onto a sick backend; after the cooldown it lets a single probe
// through (half-open) and closes again on success, or re-opens on failure.
//
// It is disabled by default (Config.Enabled=false → every call is a no-op),
// so behavior is unchanged unless configured.
package breaker

import (
	"sync"
	"time"
)

// State is the circuit state.
type State int

const (
	StateClosed   State = iota // normal: requests pass, failures are counted
	StateOpen                  // tripped: requests fail fast until cooldown elapses
	StateHalfOpen              // probing: a single request is allowed to test recovery
)

func (s State) String() string {
	switch s {
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// Config tunes breaker behavior. Enabled=false makes every breaker a no-op.
type Config struct {
	Enabled       bool
	FailThreshold int           // consecutive failures in Closed that trip Open
	Cooldown      time.Duration // how long Open lasts before a half-open probe
}

// Breaker is a single circuit (one per route). Safe for concurrent use.
type Breaker struct {
	cfg Config
	now func() time.Time // injectable clock (tests)

	mu               sync.Mutex
	state            State
	consecutiveFails int
	openedAt         time.Time
	halfOpenProbing  bool // a half-open probe is currently in flight
}

// Allow reports whether a request may proceed. The caller MUST follow an allowed
// request with exactly one RecordSuccess or RecordFailure. When disabled it
// always allows.
func (b *Breaker) Allow() bool {
	if !b.cfg.Enabled {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateOpen:
		if b.now().Sub(b.openedAt) >= b.cfg.Cooldown {
			// Cooldown elapsed: move to half-open and let this one through as the probe.
			b.state = StateHalfOpen
			b.halfOpenProbing = true
			return true
		}
		return false
	case StateHalfOpen:
		// Only a single probe at a time; everyone else fails fast.
		if b.halfOpenProbing {
			return false
		}
		b.halfOpenProbing = true
		return true
	default: // StateClosed
		return true
	}
}

// RecordSuccess reports a successful upstream call.
func (b *Breaker) RecordSuccess() {
	if !b.cfg.Enabled {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.consecutiveFails = 0
	if b.state == StateHalfOpen {
		b.state = StateClosed
		b.halfOpenProbing = false
	}
}

// RecordFailure reports a failed upstream call (error/timeout).
func (b *Breaker) RecordFailure() {
	if !b.cfg.Enabled {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateHalfOpen:
		b.trip() // probe failed → back to open
	case StateClosed:
		b.consecutiveFails++
		if b.consecutiveFails >= b.cfg.FailThreshold {
			b.trip()
		}
	}
}

// trip opens the circuit. Caller holds the lock.
func (b *Breaker) trip() {
	b.state = StateOpen
	b.openedAt = b.now()
	b.halfOpenProbing = false
}

// State returns the current state (for logging/metrics).
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Set manages one Breaker per key (route id), all sharing the same Config.
type Set struct {
	cfg      Config
	now      func() time.Time
	mu       sync.Mutex
	breakers map[string]*Breaker
}

// NewSet creates a breaker set with the given config.
func NewSet(cfg Config) *Set {
	return &Set{cfg: cfg, now: time.Now, breakers: make(map[string]*Breaker)}
}

// Get returns the breaker for key, creating it on first use.
func (s *Set) Get(key string) *Breaker {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.breakers[key]
	if !ok {
		b = &Breaker{cfg: s.cfg, now: s.now, state: StateClosed}
		s.breakers[key] = b
	}
	return b
}
