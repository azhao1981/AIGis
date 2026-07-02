package billing

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWriteUsageCSV(t *testing.T) {
	rows := []UsageRow{
		{
			Bucket:           time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			Tenant:           "tenant-a",
			Requests:         3,
			PromptTokens:     11,
			CompletionTokens: 7,
			TotalTokens:      18,
		},
	}
	rec := httptest.NewRecorder()
	writeUsageCSV(rec, rows)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q, want text/csv", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "usage.csv") {
		t.Fatalf("Content-Disposition = %q, want attachment usage.csv", cd)
	}

	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("csv has %d lines, want 2 (header + 1 row)", len(lines))
	}
	if got := strings.TrimSpace(lines[0]); got != "bucket,tenant,requests,prompt_tokens,completion_tokens,total_tokens" {
		t.Fatalf("header = %q", got)
	}
	row := strings.TrimSpace(lines[1])
	if !strings.Contains(row, "tenant-a") || !strings.Contains(row, "2026-01-02T00:00:00Z") {
		t.Fatalf("data row missing fields: %q", row)
	}
	if !strings.HasSuffix(row, "3,11,7,18") {
		t.Fatalf("data row counts wrong: %q, want ...3,11,7,18", row)
	}
}

func TestWriteUsageCSVEmpty(t *testing.T) {
	rec := httptest.NewRecorder()
	writeUsageCSV(rec, []UsageRow{})
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("empty usage csv has %d lines, want 1 (header only)", len(lines))
	}
}
