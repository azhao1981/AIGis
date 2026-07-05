// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package billing

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"

	"aigis/internal/core/usage"
)

// WAL is a disk-backed write-ahead log for usage events that could otherwise be
// lost — either because the in-memory queue overflowed (DB slow/down) or because
// a batch exhausted its DB write retries. Events are appended as JSONL to an
// active segment file; when that segment grows past maxSegBytes it is rotated to
// a timestamped ".seg" file that the replay loop later drains back into the DB.
//
// The design mirrors the audit trail's proven pattern: O_APPEND|O_CREATE with a
// 0o600 mode, a mutex serializing writes, and loud failure logging. The active
// file holds usage metadata (tenants, token counts, request IDs) — sensitive
// enough to keep owner-only. Safe for concurrent use.
type WAL struct {
	dir         string
	maxSegBytes int64
	log         *zap.Logger

	mu     sync.Mutex
	f      *os.File // active segment (nil until first Append)
	active string   // active segment path
	size   int64    // bytes written to the active segment
}

// WALRecord is one WAL line: the event plus the wall-clock time it was first
// metered. Persisting the original timestamp lets replay re-insert with the true
// ts (not the replay time), which keeps the (request_id, ts) idempotency key
// stable across replays.
type WALRecord struct {
	TS    time.Time   `json:"ts"`
	Event usage.Event `json:"event"`
}

const (
	activeSegName    = "usage.wal" // the file currently being appended to
	rotatedSegSuffix = ".seg"      // rotated, replay-pending segments
	defaultMaxSegMB  = 16          // rotate the active file past this size
)

// NewWAL opens (or creates) a WAL rooted at dir, creating the directory if
// needed. maxSegBytes <= 0 falls back to the default. A nil log is tolerated
// (guarded at every use). It does not open the active file until the first
// Append, so an idle (never-overflowing) sink touches no disk.
func NewWAL(dir string, maxSegBytes int64, log *zap.Logger) (*WAL, error) {
	if dir == "" {
		return nil, fmt.Errorf("billing: WAL dir must not be empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("billing: WAL mkdir %q: %w", dir, err)
	}
	if maxSegBytes <= 0 {
		maxSegBytes = defaultMaxSegMB * 1024 * 1024
	}
	return &WAL{
		dir:         dir,
		maxSegBytes: maxSegBytes,
		log:         log,
		active:      filepath.Join(dir, activeSegName),
	}, nil
}

// Append writes one event to the active segment, rotating first if the segment
// is already at capacity. It records the event's metering time so replay can
// preserve it. Returns an error only on a genuine disk failure (the caller logs
// it loud — a WAL write failure is the last line of defense).
func (w *WAL) Append(e usage.Event) error {
	line, err := json.Marshal(WALRecord{TS: time.Now(), Event: e})
	if err != nil {
		return fmt.Errorf("billing: WAL marshal: %w", err)
	}
	line = append(line, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensureOpenLocked(); err != nil {
		return err
	}
	if w.size > 0 && w.size+int64(len(line)) > w.maxSegBytes {
		if err := w.rotateLocked(); err != nil {
			return err
		}
		// rotateLocked closed the active file; reopen a fresh one to write into.
		if err := w.ensureOpenLocked(); err != nil {
			return err
		}
	}
	n, err := w.f.Write(line)
	w.size += int64(n)
	if err != nil {
		return fmt.Errorf("billing: WAL append: %w", err)
	}
	return nil
}

// ensureOpenLocked lazily opens the active segment. Caller holds w.mu.
func (w *WAL) ensureOpenLocked() error {
	if w.f != nil {
		return nil
	}
	f, err := os.OpenFile(w.active, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("billing: WAL open %q: %w", w.active, err)
	}
	if info, statErr := f.Stat(); statErr == nil {
		w.size = info.Size()
	}
	w.f = f
	return nil
}

// rotateLocked closes the active segment and renames it to a timestamped ".seg"
// file that the replay loop will pick up, then resets so the next Append opens a
// fresh active file. Caller holds w.mu.
func (w *WAL) rotateLocked() error {
	if w.f == nil {
		return nil
	}
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("billing: WAL close on rotate: %w", err)
	}
	w.f = nil
	w.size = 0
	// Nanosecond stamp keeps names unique even under rapid rotation.
	rotated := filepath.Join(w.dir, fmt.Sprintf("usage-%d%s", time.Now().UnixNano(), rotatedSegSuffix))
	if err := os.Rename(w.active, rotated); err != nil {
		return fmt.Errorf("billing: WAL rotate rename: %w", err)
	}
	return nil
}

// Rotate forces the active segment (if any) to become a replay-pending segment,
// so buffered events become visible to the replay loop without waiting for the
// size threshold. It is a no-op when nothing has been appended.
func (w *WAL) Rotate() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	return w.rotateLocked()
}

// PendingSegments lists the rotated ".seg" files awaiting replay, oldest first
// (their names embed a monotonic nanosecond stamp, so lexical order is age
// order).
func (w *WAL) PendingSegments() ([]string, error) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return nil, fmt.Errorf("billing: WAL list %q: %w", w.dir, err)
	}
	var segs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) == rotatedSegSuffix {
			segs = append(segs, filepath.Join(w.dir, name))
		}
	}
	sort.Strings(segs)
	return segs, nil
}

// ReadSegment decodes every WALRecord in a segment. Corrupt lines (e.g. a torn
// final write after a crash) are skipped with a warning rather than aborting the
// whole segment — one bad line must not strand all the good events behind it.
func (w *WAL) ReadSegment(path string) ([]WALRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("billing: WAL open segment %q: %w", path, err)
	}
	defer f.Close()

	var recs []WALRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		var rec WALRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			if w.log != nil {
				w.log.Warn("billing: WAL skipping corrupt line", zap.String("segment", path), zap.Error(err))
			}
			continue
		}
		recs = append(recs, rec)
	}
	if err := sc.Err(); err != nil {
		return recs, fmt.Errorf("billing: WAL scan segment %q: %w", path, err)
	}
	return recs, nil
}

// RemoveSegment deletes a fully-replayed segment.
func (w *WAL) RemoveSegment(path string) error {
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("billing: WAL remove segment %q: %w", path, err)
	}
	return nil
}

// Close flushes and closes the active segment. It does NOT rotate: the active
// file persists on disk and is picked up on the next start (its events are not
// yet in a ".seg", so a restart must also sweep the active file — see
// (*PostgresSink).replayOnce). Safe on a WAL that never opened a file.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
