// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package quota

import (
	"context"
	"testing"

	"aigis/internal/core/usage"
)

// recordingSink captures the events it receives so a test can prove the meter
// forwards to its inner sink unchanged.
type recordingSink struct {
	events []usage.Event
}

func (s *recordingSink) Record(_ context.Context, e usage.Event) {
	s.events = append(s.events, e)
}

func TestTokenMeteringSink_ForwardsAndBooks(t *testing.T) {
	inner := &recordingSink{}
	tok := &fakeToken{allow: true}
	s := NewTokenMeteringSink(inner, tok)

	e := usage.Event{Tenant: "acme", TotalTokens: 42}
	s.Record(context.Background(), e)

	if len(inner.events) != 1 || inner.events[0].TotalTokens != 42 {
		t.Fatalf("event must be forwarded to inner sink unchanged, got %+v", inner.events)
	}
	if tok.adds != 1 || tok.addTotal != 42 {
		t.Fatalf("total tokens must be booked to the gate, got adds=%d total=%d", tok.adds, tok.addTotal)
	}
}

func TestTokenMeteringSink_ZeroTokensBooksNothing(t *testing.T) {
	inner := &recordingSink{}
	tok := &fakeToken{allow: true}
	s := NewTokenMeteringSink(inner, tok)

	s.Record(context.Background(), usage.Event{Tenant: "acme", TotalTokens: 0})

	if len(inner.events) != 1 {
		t.Fatal("a 0-token event must still reach the inner sink")
	}
	if tok.adds != 0 {
		t.Fatalf("a 0-token event must book nothing, got %d Add calls", tok.adds)
	}
}

func TestTokenMeteringSink_NilGateStillForwards(t *testing.T) {
	inner := &recordingSink{}
	s := NewTokenMeteringSink(inner, nil)

	s.Record(context.Background(), usage.Event{Tenant: "acme", TotalTokens: 10})

	if len(inner.events) != 1 {
		t.Fatal("nil gate must not stop forwarding to the inner sink")
	}
}
