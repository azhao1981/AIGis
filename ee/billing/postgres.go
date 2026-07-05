// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package billing

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"aigis/internal/core/usage"
)

// Defaults for the async batching writer. Tuned so a single request never blocks
// on the DB: events are buffered and flushed by a background worker.
const (
	defaultBatchSize      = 100
	defaultFlushInterval  = 2 * time.Second
	defaultQueueSize      = 4096
	defaultMaxRetries     = 3
	retryBaseDelay        = 200 * time.Millisecond
	defaultReplayInterval = 30 * time.Second
)

// SinkOptions tunes the async writer. A zero value is filled with the package
// defaults by withDefaults, so callers may set only the fields they care about.
type SinkOptions struct {
	QueueSize     int           // buffered channel capacity (0 = default)
	BatchSize     int           // events per DB round-trip (0 = default)
	FlushInterval time.Duration // periodic flush tick (0 = default)
	MaxRetries    int           // batch write retries on DB error (<0 = none, 0 = default)

	// WALDir enables the disk write-ahead log for events that would otherwise be
	// dropped (queue overflow or write-retry exhaustion). Empty disables the WAL:
	// the sink keeps its "count but drop" behavior. When set, dropped events are
	// spooled to disk and replayed once the DB recovers, giving at-least-once
	// delivery (idempotent on request_id + ts).
	WALDir            string
	WALMaxSegBytes    int64         // rotate the active WAL segment past this size (0 = default)
	WALReplayInterval time.Duration // how often to sweep pending segments back to the DB (0 = default)
}

func (o SinkOptions) withDefaults() SinkOptions {
	if o.QueueSize <= 0 {
		o.QueueSize = defaultQueueSize
	}
	if o.BatchSize <= 0 {
		o.BatchSize = defaultBatchSize
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = defaultFlushInterval
	}
	if o.MaxRetries == 0 {
		o.MaxRetries = defaultMaxRetries
	}
	if o.MaxRetries < 0 {
		o.MaxRetries = 0
	}
	if o.WALReplayInterval <= 0 {
		o.WALReplayInterval = defaultReplayInterval
	}
	return o
}

// SinkStats is a snapshot of the sink's reliability counters. Dropped events are
// counted (never silent): Enqueued is total accepted, DroppedFull is events lost
// because the queue was full (DB slow/down + backpressure), DroppedWrite is
// events lost because a batch exhausted its write retries.
type SinkStats struct {
	Enqueued     int64 `json:"enqueued"`
	DroppedFull  int64 `json:"dropped_full"`
	DroppedWrite int64 `json:"dropped_write"`
	// WALSpooled is events written to the WAL instead of being dropped (queue
	// overflow or write-retry exhaustion). WALReplayed is events successfully
	// re-inserted from the WAL. With the WAL enabled, DroppedFull/DroppedWrite
	// count events handed to the WAL (spooled, not lost); WALSpooled tracks how
	// many actually reached disk, and WALReplayed how many were recovered.
	WALSpooled  int64 `json:"wal_spooled"`
	WALReplayed int64 `json:"wal_replayed"`
}

// PostgresSink is a usage.Sink that persists events to PostgreSQL / TimescaleDB.
// It writes ONLY standard SQL (a plain INSERT), so it is unaware of whether the
// target table is a Timescale hypertable — keeping the app portable.
//
// Writes are asynchronous: Record enqueues onto a buffered channel and returns
// immediately, so the request path is never blocked by the database. A single
// background worker drains the queue and flushes in batches (size- or
// time-triggered) via pgx.Batch.
type PostgresSink struct {
	pool *pgxpool.Pool
	log  *zap.Logger

	queue chan usage.Event
	wg    sync.WaitGroup

	batchSize     int
	flushInterval time.Duration
	maxRetries    int

	// wal, when non-nil, spools would-be-dropped events to disk and a background
	// loop replays them once the DB recovers. Nil = WAL disabled (count-and-drop).
	wal            *WAL
	replayInterval time.Duration

	// writeFn performs the actual persistence of a batch. Defaults to flushToDB;
	// tests swap it to simulate DB failures without a real database.
	writeFn func([]usage.Event) error

	// Reliability counters (never drop silently). Read via Stats.
	enqueued     atomic.Int64
	droppedFull  atomic.Int64
	droppedWrite atomic.Int64
	walSpooled   atomic.Int64
	walReplayed  atomic.Int64

	closeOnce sync.Once
	done      chan struct{}
}

// NewPostgresSink connects to the given DSN, verifies connectivity, and starts
// the background flush worker with default options. Call Close to drain and shut
// down. The caller is responsible for having applied
// migrations/001_usage_events.sql beforehand.
func NewPostgresSink(ctx context.Context, dsn string, log *zap.Logger) (*PostgresSink, error) {
	return NewPostgresSinkWithOptions(ctx, dsn, log, SinkOptions{})
}

// NewPostgresSinkWithOptions is like NewPostgresSink but lets the caller tune the
// async writer (queue size, batch size, flush interval, write retries). Zero
// fields in opts fall back to the package defaults.
func NewPostgresSinkWithOptions(ctx context.Context, dsn string, log *zap.Logger, opts SinkOptions) (*PostgresSink, error) {
	opts = opts.withDefaults()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("billing: connect: %w", err)
	}
	// Fail loud at startup if the DB is unreachable, rather than silently dropping
	// usage later.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("billing: ping: %w", err)
	}

	s := &PostgresSink{
		pool:           pool,
		log:            log,
		queue:          make(chan usage.Event, opts.QueueSize),
		batchSize:      opts.BatchSize,
		flushInterval:  opts.FlushInterval,
		maxRetries:     opts.MaxRetries,
		replayInterval: opts.WALReplayInterval,
		done:           make(chan struct{}),
	}

	if opts.WALDir != "" {
		w, err := NewWAL(opts.WALDir, opts.WALMaxSegBytes, log)
		if err != nil {
			pool.Close()
			return nil, err
		}
		s.wal = w
	}

	s.wg.Add(1)
	go s.run()

	if s.wal != nil {
		s.wg.Add(1)
		go s.replayLoop()
	}
	return s, nil
}

// Record implements usage.Sink. It enqueues the event without blocking; if the
// buffer is full (DB slow/down) the event is dropped and counted (never silent)
// rather than stalling the request path — metering must never degrade the
// gateway.
func (s *PostgresSink) Record(_ context.Context, e usage.Event) {
	select {
	case s.queue <- e:
		s.enqueued.Add(1)
	default:
		n := s.droppedFull.Add(1)
		// WAL enabled: spool to disk instead of losing the event. The counter
		// still increments (the event overflowed the queue), but the WAL gives it
		// a second life via replay, so it is not actually dropped.
		if s.spillToWAL(e) {
			return
		}
		if s.log != nil {
			s.log.Warn("billing: usage queue full, dropping event",
				zap.String("tenant", e.Tenant),
				zap.String("request_id", e.RequestID),
				zap.Int64("dropped_full_total", n),
			)
		}
	}
}

// spillToWAL appends an event to the WAL, returning true if it was spooled
// (i.e. not dropped). Returns false when the WAL is disabled, or logs loud and
// returns false on a genuine WAL write failure (the event is then truly lost —
// the last line of defense failed).
func (s *PostgresSink) spillToWAL(e usage.Event) bool {
	if s.wal == nil {
		return false
	}
	if err := s.wal.Append(e); err != nil {
		if s.log != nil {
			s.log.Error("billing: WAL append failed, event lost",
				zap.String("request_id", e.RequestID), zap.Error(err))
		}
		return false
	}
	s.walSpooled.Add(1)
	return true
}

// Stats returns a snapshot of the reliability counters.
func (s *PostgresSink) Stats() SinkStats {
	return SinkStats{
		Enqueued:     s.enqueued.Load(),
		DroppedFull:  s.droppedFull.Load(),
		DroppedWrite: s.droppedWrite.Load(),
		WALSpooled:   s.walSpooled.Load(),
		WALReplayed:  s.walReplayed.Load(),
	}
}

// run is the background worker: it accumulates events and flushes them in
// batches, triggered by batch size or a periodic tick.
func (s *PostgresSink) run() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	buf := make([]usage.Event, 0, s.batchSize)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		s.writeBatch(buf)
		buf = buf[:0]
	}

	for {
		select {
		case e := <-s.queue:
			buf = append(buf, e)
			if len(buf) >= s.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-s.done:
			// Drain whatever is still queued, then final flush.
			for {
				select {
				case e := <-s.queue:
					buf = append(buf, e)
					if len(buf) >= s.batchSize {
						flush()
					}
					continue
				default:
				}
				break
			}
			flush()
			return
		}
	}
}

// insertSQL is standard PostgreSQL — no Timescale-specific syntax. ts defaults to
// now() (the live write path). ON CONFLICT makes the write idempotent on
// (request_id, ts) so a WAL replay of the same event never double-counts; the
// partial index (request_id <> ”) means empty-request_id events are inserted
// unconditionally (no dedup, matching pre-WAL behavior).
const insertSQL = `INSERT INTO usage_events
	(ts, tenant, subject, request_id, route_id, model,
	 prompt_tokens, completion_tokens, total_tokens, streamed, success, duration_ms)
	VALUES (now(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	ON CONFLICT DO NOTHING`

// insertAtSQL is the replay writer: identical to insertSQL but takes an explicit
// ts ($1) instead of now(), so a replayed event keeps its ORIGINAL metering time.
// That is what keeps the (request_id, ts) idempotency key stable — a live insert
// and its later replay carry the same ts and collide on the unique index.
const insertAtSQL = `INSERT INTO usage_events
	(ts, tenant, subject, request_id, route_id, model,
	 prompt_tokens, completion_tokens, total_tokens, streamed, success, duration_ms)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	ON CONFLICT DO NOTHING`

// writeBatch persists a slice of events, retrying transient DB errors with
// exponential backoff before giving up. Only when all attempts fail are the
// events counted as dropped (never silent) so no data loss goes unnoticed.
func (s *PostgresSink) writeBatch(events []usage.Event) {
	write := s.writeFn
	if write == nil {
		write = s.flushToDB
	}

	var err error
	for attempt := 0; attempt <= s.maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(retryBaseDelay << (attempt - 1))
		}
		if err = write(events); err == nil {
			return
		}
		if s.log != nil {
			s.log.Warn("billing: batch insert failed, will retry",
				zap.Error(err),
				zap.Int("attempt", attempt+1),
				zap.Int("max_attempts", s.maxRetries+1),
				zap.Int("batch_size", len(events)),
			)
		}
	}

	n := s.droppedWrite.Add(int64(len(events)))
	// WAL enabled: spool the whole batch to disk instead of losing it. The replay
	// loop re-inserts each event once the DB recovers (idempotent on request_id +
	// ts). The DroppedWrite counter still increments — the batch DID fail its DB
	// write — but the WAL turns that into a deferred success, not a loss.
	spooled := 0
	for _, e := range events {
		if s.spillToWAL(e) {
			spooled++
		}
	}
	if spooled == len(events) {
		if s.log != nil {
			s.log.Warn("billing: batch insert failed after retries, spooled to WAL",
				zap.Error(err),
				zap.Int("batch_size", len(events)),
				zap.Int64("dropped_write_total", n),
			)
		}
		return
	}
	if s.log != nil {
		s.log.Error("billing: batch insert failed after retries, dropping events",
			zap.Error(err),
			zap.Int("batch_size", len(events)),
			zap.Int("spooled_to_wal", spooled),
			zap.Int64("dropped_write_total", n),
		)
	}
}

// flushToDB is the real DB write: one round-trip via pgx.Batch. It is the
// default writer; tests may swap s.writeFn to simulate failures.
func (s *PostgresSink) flushToDB(events []usage.Event) error {
	batch := &pgx.Batch{}
	for _, e := range events {
		batch.Queue(insertSQL,
			e.Tenant, e.Subject, e.RequestID, e.RouteID, e.Model,
			e.PromptTokens, e.CompletionTokens, e.TotalTokens,
			e.Streamed, e.Success, e.DurationMS,
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range events {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// replayLoop periodically sweeps the WAL back into the DB. It runs only when a
// WAL is configured. On shutdown (s.done) it makes one final replay attempt so
// events spooled just before Close still get a chance to land, then returns.
func (s *PostgresSink) replayLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.replayInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.replayOnce()
		case <-s.done:
			s.replayOnce()
			return
		}
	}
}

// replayOnce rotates the active segment (so events buffered there become a
// replay-pending ".seg") then drains every pending segment back into the DB. A
// segment is removed only after ALL its records are persisted; if the DB is
// still unreachable the segment is left on disk for the next sweep. Ordering is
// oldest-first so events replay roughly in metering order.
func (s *PostgresSink) replayOnce() {
	if s.wal == nil {
		return
	}
	// Fold the active file into the pending set so buffered events are visible;
	// a rotate error is not fatal (there may simply be no active file).
	if err := s.wal.Rotate(); err != nil && s.log != nil {
		s.log.Warn("billing: WAL rotate before replay failed", zap.Error(err))
	}

	segs, err := s.wal.PendingSegments()
	if err != nil {
		if s.log != nil {
			s.log.Warn("billing: WAL list pending segments failed", zap.Error(err))
		}
		return
	}
	for _, seg := range segs {
		if !s.replaySegment(seg) {
			// DB still unreachable: stop this sweep and retry the whole backlog
			// next tick (keeps ordering, avoids hammering a down DB).
			return
		}
	}
}

// replaySegment reads one segment and re-inserts its records with their original
// timestamps. It returns true (and deletes the segment) only if the batch was
// persisted; a DB failure returns false and leaves the segment for a later sweep.
// A read/parse failure is logged and the segment is dropped to avoid an infinite
// retry loop on a permanently corrupt file.
func (s *PostgresSink) replaySegment(path string) bool {
	recs, err := s.wal.ReadSegment(path)
	if err != nil {
		if s.log != nil {
			s.log.Error("billing: WAL read segment failed, discarding", zap.String("segment", path), zap.Error(err))
		}
		_ = s.wal.RemoveSegment(path)
		return true
	}
	if len(recs) == 0 {
		_ = s.wal.RemoveSegment(path)
		return true
	}

	if err := s.writeRecordsAt(recs); err != nil {
		if s.log != nil {
			s.log.Warn("billing: WAL replay insert failed, keeping segment",
				zap.String("segment", path), zap.Int("records", len(recs)), zap.Error(err))
		}
		return false
	}

	if err := s.wal.RemoveSegment(path); err != nil && s.log != nil {
		s.log.Warn("billing: WAL remove replayed segment failed", zap.String("segment", path), zap.Error(err))
	}
	n := s.walReplayed.Add(int64(len(recs)))
	if s.log != nil {
		s.log.Info("billing: WAL segment replayed",
			zap.String("segment", path), zap.Int("records", len(recs)), zap.Int64("wal_replayed_total", n))
	}
	return true
}

// writeRecordsAt inserts WAL records with their preserved timestamps via
// insertAtSQL. ON CONFLICT DO NOTHING makes it idempotent, so replaying a segment
// whose events already reached the DB (e.g. a partial prior replay) is harmless.
func (s *PostgresSink) writeRecordsAt(recs []WALRecord) error {
	batch := &pgx.Batch{}
	for _, r := range recs {
		e := r.Event
		batch.Queue(insertAtSQL,
			r.TS, e.Tenant, e.Subject, e.RequestID, e.RouteID, e.Model,
			e.PromptTokens, e.CompletionTokens, e.TotalTokens,
			e.Streamed, e.Success, e.DurationMS,
		)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range recs {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// UsageRow is one aggregated usage bucket returned by QueryUsage.
type UsageRow struct {
	Bucket           time.Time `json:"bucket"`
	Tenant           string    `json:"tenant"`
	Requests         int64     `json:"requests"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
}

// UsageQuery constrains a QueryUsage call. Tenant is optional (empty = all
// tenants); From/To bound the ts range; Granularity is a date_trunc unit
// ("hour"/"day"/"month", default "day").
type UsageQuery struct {
	Tenant      string
	From        time.Time
	To          time.Time
	Granularity string
}

// allowedGranularity guards the date_trunc unit against SQL injection — the unit
// cannot be a bound parameter, so it must come from this fixed allow-list.
var allowedGranularity = map[string]bool{"hour": true, "day": true, "month": true}

// QueryUsage returns per-tenant, per-bucket usage aggregates over the given
// window. It uses ONLY standard SQL (date_trunc, not Timescale's time_bucket),
// so it stays portable to a plain PostgreSQL table.
func (s *PostgresSink) QueryUsage(ctx context.Context, q UsageQuery) ([]UsageRow, error) {
	gran := q.Granularity
	if gran == "" {
		gran = "day"
	}
	if !allowedGranularity[gran] {
		return nil, fmt.Errorf("billing: invalid granularity %q", q.Granularity)
	}
	if q.To.IsZero() {
		q.To = time.Now()
	}
	if q.From.IsZero() {
		q.From = q.To.Add(-30 * 24 * time.Hour)
	}

	const queryTmpl = `SELECT date_trunc($1, ts) AS bucket, tenant,
		count(*) AS requests,
		coalesce(sum(prompt_tokens),0), coalesce(sum(completion_tokens),0), coalesce(sum(total_tokens),0)
	FROM usage_events
	WHERE ts >= $2 AND ts < $3 AND ($4 = '' OR tenant = $4)
	GROUP BY bucket, tenant
	ORDER BY bucket, tenant`

	rows, err := s.pool.Query(ctx, queryTmpl, gran, q.From, q.To, q.Tenant)
	if err != nil {
		return nil, fmt.Errorf("billing: query usage: %w", err)
	}
	defer rows.Close()

	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.Bucket, &r.Tenant, &r.Requests,
			&r.PromptTokens, &r.CompletionTokens, &r.TotalTokens); err != nil {
			return nil, fmt.Errorf("billing: scan usage row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Close stops the worker, drains the queue with a final flush, and closes the
// connection pool. Safe to call once; subsequent calls are no-ops.
func (s *PostgresSink) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.wg.Wait()
		// Close the active WAL file after both workers have stopped: the final
		// replayOnce (triggered by s.done) rotated and drained it, so anything
		// left is a fresh, empty active file. It persists on disk regardless and
		// is swept on the next start.
		if s.wal != nil {
			if err := s.wal.Close(); err != nil && s.log != nil {
				s.log.Warn("billing: WAL close failed", zap.Error(err))
			}
		}
		if s.log != nil {
			st := s.Stats()
			s.log.Info("billing: sink closed",
				zap.Int64("enqueued", st.Enqueued),
				zap.Int64("dropped_full", st.DroppedFull),
				zap.Int64("dropped_write", st.DroppedWrite),
				zap.Int64("wal_spooled", st.WALSpooled),
				zap.Int64("wal_replayed", st.WALReplayed),
			)
		}
		s.pool.Close()
	})
}
