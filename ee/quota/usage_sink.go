// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package quota

import (
	"context"

	"aigis/internal/core/usage"
)

// TokenMeteringSink decorates a usage.Sink so that, in addition to whatever the
// inner sink does (e.g. persist to the billing store), every completed request
// books its spent tokens against the tenant's token budget. This is the write
// half of the token quota: the read half is TokenLimiter.Allow, consulted at
// admission time by TenantLimiter. Splitting them lets the core stay unchanged
// — it already calls usageSink.Record after the response, when TotalTokens is
// known, so wrapping the sink is the whole integration.
//
// It implements usage.Sink, so it plugs into the core via server.SetUsageSink
// exactly like the plain billing sink it wraps.
type TokenMeteringSink struct {
	inner usage.Sink
	token TokenGate
}

// NewTokenMeteringSink wraps inner so each recorded Event's TotalTokens is added
// to the tenant's token tally via gate. inner is the sink that would otherwise
// be installed (billing / metering); gate is the same TokenGate the admission
// limiter reads. Both must be non-nil.
func NewTokenMeteringSink(inner usage.Sink, gate TokenGate) *TokenMeteringSink {
	return &TokenMeteringSink{inner: inner, token: gate}
}

// Record forwards the event to the inner sink, then books its total tokens
// against the tenant's budget. Booking is a best-effort side effect: it never
// blocks or fails the inner Record. A 0-token event (e.g. a streamed response
// with no usage, or an error) books nothing.
func (s *TokenMeteringSink) Record(ctx context.Context, e usage.Event) {
	s.inner.Record(ctx, e)
	if s.token != nil && e.TotalTokens > 0 {
		s.token.Add(e.Tenant, e.TotalTokens)
	}
}
