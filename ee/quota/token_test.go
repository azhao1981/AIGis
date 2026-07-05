// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package quota

import (
	"testing"
	"time"
)

// clockAt returns an injectable now func pinned to t, so tests drive period
// roll-over deterministically without sleeping.
func clockAt(t *time.Time) func() time.Time {
	return func() time.Time { return *t }
}

func TestTokenLimiter_AccumulatesToCeiling(t *testing.T) {
	l := NewTokenLimiter(nil, 100, PeriodDay)

	if !l.Allow("acme") {
		t.Fatal("fresh period must admit")
	}
	l.Add("acme", 60)
	if !l.Allow("acme") {
		t.Fatal("60/100 spent must still admit")
	}
	l.Add("acme", 40) // now exactly at the ceiling
	if l.Allow("acme") {
		t.Fatal("100/100 spent must be rejected (used >= max)")
	}
}

func TestTokenLimiter_ResetsAcrossPeriod(t *testing.T) {
	now := time.Date(2026, 1, 10, 8, 0, 0, 0, time.UTC)
	l := NewTokenLimiter(nil, 50, PeriodDay)
	l.now = clockAt(&now)

	l.Add("acme", 50)
	if l.Allow("acme") {
		t.Fatal("ceiling hit within the day must reject")
	}

	// Cross into the next UTC day: the tally must reset.
	now = time.Date(2026, 1, 11, 0, 0, 1, 0, time.UTC)
	if !l.Allow("acme") {
		t.Fatal("new day must reset the token tally")
	}
}

func TestTokenLimiter_HourPeriodResets(t *testing.T) {
	now := time.Date(2026, 1, 10, 8, 30, 0, 0, time.UTC)
	l := NewTokenLimiter(nil, 10, PeriodHour)
	l.now = clockAt(&now)

	l.Add("acme", 10)
	if l.Allow("acme") {
		t.Fatal("hour ceiling hit must reject")
	}
	now = time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
	if !l.Allow("acme") {
		t.Fatal("next hour must reset")
	}
}

func TestTokenLimiter_PerTenantIsolation(t *testing.T) {
	l := NewTokenLimiter(map[string]int{"vip": 1000}, 100, PeriodDay)

	l.Add("acme", 100)
	if l.Allow("acme") {
		t.Fatal("acme (default 100) must be capped")
	}
	// vip has its own higher ceiling and its own tally.
	l.Add("vip", 100)
	if !l.Allow("vip") {
		t.Fatal("vip (override 1000) must still admit at 100 spent")
	}
}

func TestTokenLimiter_UnlimitedNeverCounts(t *testing.T) {
	l := NewTokenLimiter(nil, 0, PeriodDay) // 0 = unlimited

	l.Add("acme", 1_000_000)
	if !l.Allow("acme") {
		t.Fatal("unlimited tenant must always admit")
	}
	// Unlimited tenants aren't tracked at all.
	if _, ok := l.window["acme"]; ok {
		t.Fatal("unlimited tenant must not allocate a window")
	}
}

func TestTokenLimiter_AddFlipsAllow(t *testing.T) {
	l := NewTokenLimiter(nil, 30, PeriodDay)

	if !l.Allow("acme") {
		t.Fatal("must start admitting")
	}
	l.Add("acme", 29)
	if !l.Allow("acme") {
		t.Fatal("29/30 must still admit")
	}
	l.Add("acme", 1) // 30/30
	if l.Allow("acme") {
		t.Fatal("Add crossing the ceiling must flip Allow to false")
	}
}

func TestParsePeriod(t *testing.T) {
	cases := map[string]Period{
		"":         PeriodDay,
		"day":      PeriodDay,
		"DAY":      PeriodDay,
		" hour ":   PeriodHour,
		"month":    PeriodMonth,
		"nonsense": PeriodDay,
	}
	for in, want := range cases {
		if got := ParsePeriod(in); got != want {
			t.Fatalf("ParsePeriod(%q) = %v, want %v", in, got, want)
		}
	}
}
