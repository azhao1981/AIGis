// Package usage defines the gateway's per-request usage-metering seam. The
// open-source core emits a usage Event at the end of every request through a
// Sink; the default Sink is a no-op, so the open-source build measures nothing
// and stays dependency-free. The Enterprise Edition plugs in its own Sink (e.g.
// billing / metering) via server.SetUsageSink — a one-way ee -> core extension,
// mirroring the auth middleware seam.
package usage

import "context"

// Event is a single request's usage record. Token counts are best-effort: they
// are parsed from the upstream response's "usage" object and are 0 when absent
// (e.g. streaming responses that omit usage, or errors).
type Event struct {
	Tenant           string
	Subject          string
	RequestID        string
	RouteID          string
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Streamed         bool
	Success          bool
	DurationMS       int64
}

// Sink consumes usage Events. Implementations must be safe for concurrent use
// and must not block the request path (buffer/async internally if needed).
type Sink interface {
	Record(ctx context.Context, e Event)
}

// NopSink is the default Sink: it discards every Event. It is what the
// open-source build uses, so metering is inert unless an Enterprise Sink is set.
type NopSink struct{}

// Record implements Sink by doing nothing.
func (NopSink) Record(context.Context, Event) {}
