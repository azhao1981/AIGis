package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"aigis/internal/core"
)

// readLines returns the JSON lines written to the audit file.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open audit file: %v", err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func newCtxWithDetections(reqID string, dets [][3]string) *core.AIGisContext {
	ctx := core.NewGatewayContext(context.Background(), nil)
	ctx.RequestID = reqID
	ctx.TraceID = "trace-" + reqID
	for _, d := range dets {
		ctx.RecordDetection(d[0], d[1], d[2]) // [type, placeholder, preview]
	}
	return ctx
}

func TestRecord_WritesMetadataOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a, err := New(path, true, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	secret := "sk-realsecretkey1234567890"
	ctx := newCtxWithDetections("req1", [][3]string{
		{"Email", "__AIGIS_SEC_aaaaaaaaaaaa__", "te***om"},
		{"Email", "__AIGIS_SEC_bbbbbbbbbbbb__", "fo***ar"},
		{"OpenAI API Key", "__AIGIS_SEC_cccccccccccc__", "sk***90"},
	})
	ctx.SetMetadata("model", "glm-4.6")
	ctx.SetMetadata("route_id", "claude-proxy")
	// Vault holds the plaintext, but it must NEVER leak into the audit line.
	ctx.VaultStore("__AIGIS_SEC_cccccccccccc__", secret)

	a.Record(ctx)
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("expected 1 audit line, got %d", len(lines))
	}

	// No plaintext secret anywhere in the line.
	if strings.Contains(lines[0], secret) {
		t.Fatalf("audit line must not contain plaintext secret: %s", lines[0])
	}

	var rec Record
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec.Total != 3 {
		t.Errorf("Total = %d, want 3", rec.Total)
	}
	if rec.ByType["Email"] != 2 || rec.ByType["OpenAI API Key"] != 1 {
		t.Errorf("ByType = %v, want Email:2 OpenAI API Key:1", rec.ByType)
	}
	if len(rec.Items) != 3 {
		t.Errorf("Items len = %d, want 3", len(rec.Items))
	}
	// Preview must round-trip and must NOT be the full secret.
	if rec.Items[2].Preview != "sk***90" {
		t.Errorf("Items[2].Preview = %q, want sk***90", rec.Items[2].Preview)
	}
	for _, it := range rec.Items {
		if strings.Contains(secret, it.Preview) && len(it.Preview) > 4 {
			t.Errorf("preview %q looks like a substring of the secret", it.Preview)
		}
	}
	if rec.Model != "glm-4.6" || rec.RouteID != "claude-proxy" {
		t.Errorf("Model/RouteID = %q/%q, want glm-4.6/claude-proxy", rec.Model, rec.RouteID)
	}
	if rec.RequestID != "req1" || rec.TraceID != "trace-req1" {
		t.Errorf("RequestID/TraceID = %q/%q", rec.RequestID, rec.TraceID)
	}
}

func TestRecord_CleanRequestWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a, err := New(path, true, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.Record(newCtxWithDetections("clean", nil)) // no detections
	a.Close()

	// New() created the file (O_CREATE); a clean request must add no lines.
	if lines := readLines(t, path); len(lines) != 0 {
		t.Fatalf("clean request should write no line, got %d", len(lines))
	}
}

func TestRecord_WriteFailureIsLoud(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	core_, obs := observer.New(zap.ErrorLevel)
	a, err := New(path, true, zap.New(core_))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Force a write failure: close the underlying file out from under Record.
	if err := a.w.Close(); err != nil {
		t.Fatalf("close fd: %v", err)
	}

	a.Record(newCtxWithDetections("req-fail", [][3]string{
		{"Email", "__AIGIS_SEC_aaaaaaaaaaaa__", "te***om"},
	}))

	logs := obs.FilterMessage("audit: write failed").All()
	if len(logs) != 1 {
		t.Fatalf("expected 1 'audit: write failed' error log, got %d", len(logs))
	}
	if rid, ok := logs[0].ContextMap()["request_id"]; !ok || rid != "req-fail" {
		t.Errorf("write-failure log missing request_id, got %v", logs[0].ContextMap())
	}
}

func TestRecord_DisabledIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a, err := New(path, false, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.Record(newCtxWithDetections("x", [][3]string{{"Email", "__AIGIS_SEC_aaaaaaaaaaaa__", "te***om"}}))
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled auditor must not create file, stat err = %v", err)
	}
}

// TestNewWithRotation_RotatesBySize verifies the lumberjack-backed auditor
// rotates the live file once it exceeds MaxSizeMB, keeps writing to the fresh
// current file (so /admin/audit queries stay valid), and never loses records.
func TestNewWithRotation_RotatesBySize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	a, err := NewWithRotation(path, true, Rotation{Enabled: true, MaxSizeMB: 1, MaxBackups: 2}, nil)
	if err != nil {
		t.Fatalf("NewWithRotation: %v", err)
	}

	// Each record is ~1KB of preview padding; ~1100 of them crosses 1MB.
	bigPreview := strings.Repeat("x", 1024)
	for i := 0; i < 1100; i++ {
		a.Record(newCtxWithDetections("req", [][3]string{{"Email", "__AIGIS_SEC_aaaaaaaaaaaa__", bigPreview}}))
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The live file must exist and be under the cap (rotation happened).
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat live file: %v", err)
	}
	if st.Size() > 1<<20+4096 {
		t.Errorf("live file not rotated: %d bytes", st.Size())
	}

	// At least one rotated backup exists alongside.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) < 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected live + rotated file(s), got %v", names)
	}

	// The current file remains valid JSONL for Query.
	recs, err := Query(path, QueryOptions{Limit: 5})
	if err != nil {
		t.Fatalf("Query after rotation: %v", err)
	}
	if len(recs) == 0 {
		t.Error("Query returned no records from the post-rotation live file")
	}
}
