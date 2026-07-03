// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package billing

import (
	"os"
	"path/filepath"
	"testing"

	"aigis/internal/core/usage"
)

// appendN appends n events (request IDs r0..r{n-1}) to the WAL, failing the test
// on any write error.
func appendN(t *testing.T, w *WAL, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := w.Append(usage.Event{Tenant: "acme", RequestID: reqID(i), TotalTokens: i}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
}

func reqID(i int) string { return "r" + string(rune('0'+i)) }

func TestWALAppendRotateReadBack(t *testing.T) {
	dir := t.TempDir()
	// Tiny segment so each append forces a rotation, exercising the rotate path.
	w, err := NewWAL(dir, 1, nil)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer w.Close()

	appendN(t, w, 3)

	// Fold the still-active file into the pending set.
	if err := w.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	segs, err := w.PendingSegments()
	if err != nil {
		t.Fatalf("PendingSegments: %v", err)
	}
	if len(segs) == 0 {
		t.Fatal("expected at least one pending segment")
	}

	// Read every segment back; total records must equal what we appended and the
	// preserved timestamps must be non-zero.
	var total int
	for _, seg := range segs {
		recs, err := w.ReadSegment(seg)
		if err != nil {
			t.Fatalf("ReadSegment %s: %v", seg, err)
		}
		for _, r := range recs {
			if r.TS.IsZero() {
				t.Errorf("record TS is zero, want preserved metering time")
			}
			if r.Event.Tenant != "acme" {
				t.Errorf("Event.Tenant = %q, want acme", r.Event.Tenant)
			}
		}
		total += len(recs)
	}
	if total != 3 {
		t.Errorf("read-back records = %d, want 3", total)
	}
}

func TestWALSkipsCorruptLine(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir, 0, nil)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer w.Close()

	appendN(t, w, 2)
	if err := w.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	segs, err := w.PendingSegments()
	if err != nil || len(segs) != 1 {
		t.Fatalf("PendingSegments = %v (err %v), want 1 segment", segs, err)
	}

	// Corrupt the segment by prepending a torn line, then a valid one after.
	seg := segs[0]
	orig, err := os.ReadFile(seg)
	if err != nil {
		t.Fatalf("read seg: %v", err)
	}
	tampered := append([]byte("{not valid json\n"), orig...)
	if err := os.WriteFile(seg, tampered, 0o600); err != nil {
		t.Fatalf("write seg: %v", err)
	}

	recs, err := w.ReadSegment(seg)
	if err != nil {
		t.Fatalf("ReadSegment: %v", err)
	}
	// The corrupt line is skipped; the two valid records survive.
	if len(recs) != 2 {
		t.Errorf("records = %d, want 2 (corrupt line skipped)", len(recs))
	}
}

func TestWALRotateNoopWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir, 0, nil)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer w.Close()

	// Nothing appended: Rotate must be a no-op and produce no segments.
	if err := w.Rotate(); err != nil {
		t.Fatalf("Rotate on empty: %v", err)
	}
	segs, err := w.PendingSegments()
	if err != nil {
		t.Fatalf("PendingSegments: %v", err)
	}
	if len(segs) != 0 {
		t.Errorf("segments = %d, want 0", len(segs))
	}
}

func TestWALCloseKeepsActiveFile(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir, 0, nil)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	appendN(t, w, 1)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Close must NOT rotate: the active file persists for the next-start sweep.
	if _, err := os.Stat(filepath.Join(dir, activeSegName)); err != nil {
		t.Errorf("active file missing after Close: %v", err)
	}
	if segs, _ := w.PendingSegments(); len(segs) != 0 {
		t.Errorf("Close rotated into %d segments, want 0", len(segs))
	}
}

func TestWALRemoveSegment(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir, 0, nil)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer w.Close()

	appendN(t, w, 1)
	if err := w.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	segs, _ := w.PendingSegments()
	if len(segs) != 1 {
		t.Fatalf("want 1 segment, got %d", len(segs))
	}
	if err := w.RemoveSegment(segs[0]); err != nil {
		t.Fatalf("RemoveSegment: %v", err)
	}
	if segs, _ := w.PendingSegments(); len(segs) != 0 {
		t.Errorf("segment still present after RemoveSegment: %d", len(segs))
	}
}
