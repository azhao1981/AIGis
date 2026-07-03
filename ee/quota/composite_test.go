// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package quota

import (
	"testing"

	corequota "aigis/internal/core/quota"
)

// fakeRate is a RateGate whose verdict is fixed per construction, recording how
// many times it was consulted so a test can assert the QPS gate ran (or didn't).
type fakeRate struct {
	allow bool
	calls int
}

func (f *fakeRate) Allow(string) bool { f.calls++; return f.allow }

// fakeConcurrency is a corequota.Limiter with a fixed verdict that counts
// Acquire calls and Release invocations, so a test can prove a QPS rejection
// never reached (nor reserved a slot in) the concurrency gate.
type fakeConcurrency struct {
	admit    bool
	acquires int
	releases int
}

func (f *fakeConcurrency) Acquire(string) (corequota.Release, bool) {
	f.acquires++
	if !f.admit {
		return func() {}, false
	}
	return func() { f.releases++ }, true
}

// fakeToken is a TokenGate with a fixed Allow verdict, counting Allow checks and
// Add bookings so a test can prove the token gate ran at admission but is not
// booked during Acquire (tokens are booked by the sink after the response).
type fakeToken struct {
	allow    bool
	allows   int
	adds     int
	addTotal int
}

func (f *fakeToken) Allow(string) bool { f.allows++; return f.allow }
func (f *fakeToken) Add(_ string, n int) { f.adds++; f.addTotal += n }

func TestTenantLimiter_QPSRejectSkipsConcurrency(t *testing.T) {
	rate := &fakeRate{allow: false}
	cc := &fakeConcurrency{admit: true}
	l := NewTenantLimiter(rate, nil, cc)

	rel, ok := l.Acquire("acme")
	if ok {
		t.Fatal("request must be rejected when the QPS gate denies it")
	}
	if cc.acquires != 0 {
		t.Fatalf("concurrency gate must not be consulted on QPS rejection, got %d acquires", cc.acquires)
	}
	// The returned Release must be a safe no-op on rejection.
	rel()
	if cc.releases != 0 {
		t.Fatalf("rejection Release must not free a concurrency slot, got %d releases", cc.releases)
	}
}

func TestTenantLimiter_ConcurrencyReject(t *testing.T) {
	rate := &fakeRate{allow: true}
	cc := &fakeConcurrency{admit: false}
	l := NewTenantLimiter(rate, nil, cc)

	if _, ok := l.Acquire("acme"); ok {
		t.Fatal("request must be rejected when the concurrency gate denies it")
	}
	if rate.calls != 1 {
		t.Fatalf("QPS gate must be consulted exactly once, got %d", rate.calls)
	}
	if cc.acquires != 1 {
		t.Fatalf("concurrency gate must be consulted after passing QPS, got %d", cc.acquires)
	}
}

func TestTenantLimiter_BothPassReleasesConcurrency(t *testing.T) {
	rate := &fakeRate{allow: true}
	cc := &fakeConcurrency{admit: true}
	l := NewTenantLimiter(rate, nil, cc)

	rel, ok := l.Acquire("acme")
	if !ok {
		t.Fatal("request must be admitted when both gates pass")
	}
	rel()
	if cc.releases != 1 {
		t.Fatalf("Release must free exactly one concurrency slot, got %d", cc.releases)
	}
}

func TestTenantLimiter_RateOnly(t *testing.T) {
	rate := &fakeRate{allow: true}
	l := NewTenantLimiter(rate, nil, nil) // concurrency disabled

	rel, ok := l.Acquire("acme")
	if !ok {
		t.Fatal("request must be admitted when QPS passes and concurrency is disabled")
	}
	rel() // must be a safe no-op

	rate.allow = false
	if _, ok := l.Acquire("acme"); ok {
		t.Fatal("request must be rejected when the sole QPS gate denies it")
	}
}

func TestTenantLimiter_ConcurrencyOnly(t *testing.T) {
	cc := &fakeConcurrency{admit: true}
	l := NewTenantLimiter(nil, nil, cc) // QPS disabled

	rel, ok := l.Acquire("acme")
	if !ok {
		t.Fatal("request must be admitted when concurrency passes and QPS is disabled")
	}
	rel()
	if cc.acquires != 1 || cc.releases != 1 {
		t.Fatalf("concurrency gate should own the whole decision, got acquires=%d releases=%d", cc.acquires, cc.releases)
	}
}

func TestTenantLimiter_BothNilAdmitsAll(t *testing.T) {
	l := NewTenantLimiter(nil, nil, nil)
	rel, ok := l.Acquire("acme")
	if !ok {
		t.Fatal("a limiter with no gates must admit everything")
	}
	rel() // no-op
}

func TestTenantLimiter_TokenRejectSkipsConcurrency(t *testing.T) {
	rate := &fakeRate{allow: true}
	tok := &fakeToken{allow: false}
	cc := &fakeConcurrency{admit: true}
	l := NewTenantLimiter(rate, tok, cc)

	if _, ok := l.Acquire("acme"); ok {
		t.Fatal("request must be rejected when the token gate denies it")
	}
	if tok.allows != 1 {
		t.Fatalf("token gate must be consulted once after QPS, got %d", tok.allows)
	}
	if cc.acquires != 0 {
		t.Fatalf("concurrency must not be reached on token rejection, got %d acquires", cc.acquires)
	}
}

func TestTenantLimiter_TokenAcquireNeverBooks(t *testing.T) {
	tok := &fakeToken{allow: true}
	l := NewTenantLimiter(nil, tok, nil)

	if _, ok := l.Acquire("acme"); !ok {
		t.Fatal("request must be admitted when the token gate allows it")
	}
	// Acquire is an event-time check only: booking happens in the sink, not here.
	if tok.adds != 0 {
		t.Fatalf("Acquire must not book tokens, got %d Add calls", tok.adds)
	}
}

func TestTenantLimiter_QPSRejectSkipsToken(t *testing.T) {
	rate := &fakeRate{allow: false}
	tok := &fakeToken{allow: true}
	l := NewTenantLimiter(rate, tok, nil)

	if _, ok := l.Acquire("acme"); ok {
		t.Fatal("request must be rejected on QPS before the token gate")
	}
	if tok.allows != 0 {
		t.Fatalf("token gate must not be consulted on QPS rejection, got %d", tok.allows)
	}
}
