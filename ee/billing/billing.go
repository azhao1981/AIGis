// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

// Package billing is the Enterprise Edition usage-metering sink. It implements
// the open-source core's usage.Sink seam (injected via server.SetUsageSink), so
// the dependency is one-way (ee -> core). This starter keeps per-tenant running
// totals in memory and logs each event; a real deployment would persist to a
// metering store / emit to a billing pipeline.
package billing

import (
	"context"
	"sync"

	"go.uber.org/zap"

	"aigis/internal/core/usage"
)

// TenantTotals is a running per-tenant aggregate of metered usage.
type TenantTotals struct {
	Requests         int64
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// MeteringSink is an in-memory usage.Sink: it accumulates per-tenant totals and
// logs every event. Safe for concurrent use.
type MeteringSink struct {
	log *zap.Logger

	mu     sync.Mutex
	totals map[string]*TenantTotals
}

// NewMeteringSink builds a metering sink. log may be nil (events are then only
// aggregated, not logged).
func NewMeteringSink(log *zap.Logger) *MeteringSink {
	return &MeteringSink{log: log, totals: make(map[string]*TenantTotals)}
}

// Record implements usage.Sink: it folds the event into the tenant's totals and
// logs a structured metering line. It never blocks meaningfully (in-memory).
func (m *MeteringSink) Record(_ context.Context, e usage.Event) {
	m.mu.Lock()
	t, ok := m.totals[e.Tenant]
	if !ok {
		t = &TenantTotals{}
		m.totals[e.Tenant] = t
	}
	t.Requests++
	t.PromptTokens += int64(e.PromptTokens)
	t.CompletionTokens += int64(e.CompletionTokens)
	t.TotalTokens += int64(e.TotalTokens)
	snapshot := *t
	m.mu.Unlock()

	if m.log != nil {
		m.log.Info("usage metered",
			zap.String("tenant", e.Tenant),
			zap.String("subject", e.Subject),
			zap.String("request_id", e.RequestID),
			zap.String("route_id", e.RouteID),
			zap.String("model", e.Model),
			zap.Bool("streamed", e.Streamed),
			zap.Bool("success", e.Success),
			zap.Int("prompt_tokens", e.PromptTokens),
			zap.Int("completion_tokens", e.CompletionTokens),
			zap.Int("total_tokens", e.TotalTokens),
			zap.Int64("duration_ms", e.DurationMS),
			zap.Int64("tenant_total_tokens", snapshot.TotalTokens),
			zap.Int64("tenant_requests", snapshot.Requests),
		)
	}
}

// Totals returns a snapshot copy of the current per-tenant totals.
func (m *MeteringSink) Totals() map[string]TenantTotals {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]TenantTotals, len(m.totals))
	for k, v := range m.totals {
		out[k] = *v
	}
	return out
}
