// Package audit persists records of sensitive information that the gateway
// masked, for compliance/audit purposes. It NEVER writes full plaintext secrets
// — only the rule type, counts, placeholders, and partially-masked previews.
//
// The audit file is therefore sensitive (previews reveal a few boundary chars,
// and placeholders are a stable hash fingerprint of each secret). It is created
// 0o600 (owner-only); protect it at the storage/access-control layer too.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"

	"aigis/internal/core"
)

// Item is one masked hit: rule type, its placeholder, and a partially-masked
// preview (e.g. "te***om"). Preview never contains the full plaintext.
type Item struct {
	Type        string `json:"type"`
	Placeholder string `json:"placeholder"`
	Preview     string `json:"preview"`
}

// Record is one audit line: a per-request summary of what was masked.
// No full plaintext secret ever appears here (previews are partially masked).
type Record struct {
	RequestID  string         `json:"request_id"`
	TraceID    string         `json:"trace_id"`
	Timestamp  string         `json:"timestamp"` // RFC3339
	Tenant     string         `json:"tenant,omitempty"`
	Model      string         `json:"model,omitempty"`
	RouteID    string         `json:"route_id,omitempty"`
	Total      int            `json:"total"`
	ByType     map[string]int `json:"by_type"`
	Items      []Item         `json:"items"`
	DurationMS int64          `json:"duration_ms"`
}

// Auditor appends Records as JSON lines (JSONL) to a file. Safe for concurrent use.
type Auditor struct {
	mu      sync.Mutex
	f       *os.File
	enabled bool
	log     *zap.Logger // may be nil; guarded at every use
}

// New opens (or creates) the audit file in append mode. When enabled is false the
// returned Auditor is a no-op and no file is opened. log records write failures
// so a silently-broken audit trail is loud (it may be nil).
func New(path string, enabled bool, log *zap.Logger) (*Auditor, error) {
	if !enabled {
		return &Auditor{enabled: false, log: log}, nil
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("audit: failed to create dir %q: %w", dir, err)
		}
	}
	// 0o600: the audit file holds secret previews + fingerprints, so keep it owner-only.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: failed to open %q: %w", path, err)
	}
	return &Auditor{f: f, enabled: true, log: log}, nil
}

// Record writes one audit line for the request, aggregating the context's
// detections. It is a no-op when auditing is disabled or nothing was masked
// (clean requests produce no line).
func (a *Auditor) Record(ctx *core.AIGisContext) {
	if a == nil || !a.enabled || ctx == nil {
		return
	}

	detections := ctx.Detections()
	if len(detections) == 0 {
		return
	}

	byType := make(map[string]int, len(detections))
	items := make([]Item, 0, len(detections))
	for _, d := range detections {
		byType[d.Type]++
		items = append(items, Item{Type: d.Type, Placeholder: d.Placeholder, Preview: d.Preview})
	}

	now := time.Now()
	rec := Record{
		RequestID:  ctx.RequestID,
		TraceID:    ctx.TraceID,
		Timestamp:  now.Format(time.RFC3339),
		Tenant:     ctx.Tenant,
		Total:      len(detections),
		ByType:     byType,
		Items:      items,
		DurationMS: now.Sub(ctx.StartTime).Milliseconds(),
	}
	if model, ok := ctx.GetMetadata("model"); ok {
		if s, ok := model.(string); ok {
			rec.Model = s
		}
	}
	if routeID, ok := ctx.GetMetadata("route_id"); ok {
		if s, ok := routeID.(string); ok {
			rec.RouteID = s
		}
	}

	line, err := json.Marshal(rec)
	if err != nil {
		// Don't crash the request, but don't fail silently either.
		if a.log != nil {
			a.log.Error("audit: marshal failed", zap.String("request_id", ctx.RequestID), zap.Error(err))
		}
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.f.Write(append(line, '\n')); err != nil {
		// A broken audit trail (disk full, perms, device error) must be loud.
		if a.log != nil {
			a.log.Error("audit: write failed", zap.String("request_id", ctx.RequestID), zap.Error(err))
		}
	}
}

// Close closes the underlying file. Safe to call on a disabled/nil Auditor.
func (a *Auditor) Close() error {
	if a == nil || !a.enabled || a.f == nil {
		return nil
	}
	return a.f.Close()
}
