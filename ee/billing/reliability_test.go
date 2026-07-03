// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package billing

import (
	"errors"
	"testing"
	"time"

	"aigis/internal/core/usage"
)

func TestSinkOptionsWithDefaults(t *testing.T) {
	got := SinkOptions{}.withDefaults()
	if got.QueueSize != defaultQueueSize {
		t.Errorf("QueueSize = %d, want %d", got.QueueSize, defaultQueueSize)
	}
	if got.BatchSize != defaultBatchSize {
		t.Errorf("BatchSize = %d, want %d", got.BatchSize, defaultBatchSize)
	}
	if got.FlushInterval != defaultFlushInterval {
		t.Errorf("FlushInterval = %v, want %v", got.FlushInterval, defaultFlushInterval)
	}
	if got.MaxRetries != defaultMaxRetries {
		t.Errorf("MaxRetries = %d, want %d", got.MaxRetries, defaultMaxRetries)
	}

	custom := SinkOptions{QueueSize: 7, BatchSize: 9, FlushInterval: time.Second, MaxRetries: 5}.withDefaults()
	if custom.QueueSize != 7 || custom.BatchSize != 9 || custom.FlushInterval != time.Second || custom.MaxRetries != 5 {
		t.Errorf("explicit options overwritten: %+v", custom)
	}

	// A negative MaxRetries means "no retries" and must be preserved as 0.
	if none := (SinkOptions{MaxRetries: -1}).withDefaults(); none.MaxRetries != 0 {
		t.Errorf("MaxRetries=-1 -> %d, want 0", none.MaxRetries)
	}
}

func TestWriteBatchRetriesThenSucceeds(t *testing.T) {
	var calls int
	s := &PostgresSink{
		maxRetries: 3,
		writeFn: func([]usage.Event) error {
			calls++
			if calls < 3 {
				return errors.New("transient db error")
			}
			return nil
		},
	}

	s.writeBatch([]usage.Event{{Tenant: "acme"}, {Tenant: "acme"}})

	if calls != 3 {
		t.Errorf("write attempts = %d, want 3", calls)
	}
	if got := s.Stats().DroppedWrite; got != 0 {
		t.Errorf("DroppedWrite = %d, want 0 (eventual success)", got)
	}
}

func TestWriteBatchExhaustsRetriesAndCountsDrop(t *testing.T) {
	var calls int
	s := &PostgresSink{
		maxRetries: 2, // total 3 attempts
		writeFn: func([]usage.Event) error {
			calls++
			return errors.New("db down")
		},
	}

	events := []usage.Event{{Tenant: "acme"}, {Tenant: "globex"}, {Tenant: "acme"}}
	s.writeBatch(events)

	if calls != 3 {
		t.Errorf("write attempts = %d, want 3 (1 + 2 retries)", calls)
	}
	if got := s.Stats().DroppedWrite; got != int64(len(events)) {
		t.Errorf("DroppedWrite = %d, want %d (whole batch)", got, len(events))
	}
}

func TestRecordCountsEnqueuedAndDroppedFull(t *testing.T) {
	// Capacity 1, no worker draining: first Record enqueues, the rest overflow.
	s := &PostgresSink{queue: make(chan usage.Event, 1)}

	s.Record(t.Context(), usage.Event{Tenant: "acme", RequestID: "r1"})
	s.Record(t.Context(), usage.Event{Tenant: "acme", RequestID: "r2"})
	s.Record(t.Context(), usage.Event{Tenant: "acme", RequestID: "r3"})

	st := s.Stats()
	if st.Enqueued != 1 {
		t.Errorf("Enqueued = %d, want 1", st.Enqueued)
	}
	if st.DroppedFull != 2 {
		t.Errorf("DroppedFull = %d, want 2", st.DroppedFull)
	}
}

func TestRecordOverflowSpoolsToWAL(t *testing.T) {
	// Queue-full events must land in the WAL instead of being lost.
	w, err := NewWAL(t.TempDir(), 0, nil)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer w.Close()

	s := &PostgresSink{queue: make(chan usage.Event, 1), wal: w}
	s.Record(t.Context(), usage.Event{Tenant: "acme", RequestID: "r1"}) // enqueued
	s.Record(t.Context(), usage.Event{Tenant: "acme", RequestID: "r2"}) // overflow -> WAL
	s.Record(t.Context(), usage.Event{Tenant: "acme", RequestID: "r3"}) // overflow -> WAL

	st := s.Stats()
	if st.DroppedFull != 2 {
		t.Errorf("DroppedFull = %d, want 2", st.DroppedFull)
	}
	if st.WALSpooled != 2 {
		t.Errorf("WALSpooled = %d, want 2", st.WALSpooled)
	}

	// The two overflow events must be recoverable from the WAL.
	if err := w.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	segs, _ := w.PendingSegments()
	var total int
	for _, seg := range segs {
		recs, _ := w.ReadSegment(seg)
		total += len(recs)
	}
	if total != 2 {
		t.Errorf("spooled records = %d, want 2", total)
	}
}

func TestWriteBatchExhaustedSpoolsToWAL(t *testing.T) {
	w, err := NewWAL(t.TempDir(), 0, nil)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer w.Close()

	s := &PostgresSink{
		maxRetries: 1,
		wal:        w,
		writeFn:    func([]usage.Event) error { return errors.New("db down") },
	}
	events := []usage.Event{{RequestID: "a"}, {RequestID: "b"}, {RequestID: "c"}}
	s.writeBatch(events)

	st := s.Stats()
	if st.DroppedWrite != int64(len(events)) {
		t.Errorf("DroppedWrite = %d, want %d", st.DroppedWrite, len(events))
	}
	if st.WALSpooled != int64(len(events)) {
		t.Errorf("WALSpooled = %d, want %d (whole batch spooled)", st.WALSpooled, len(events))
	}
}
