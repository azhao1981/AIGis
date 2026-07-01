// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package billing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"aigis/internal/core/usage"
)

// Defaults for the async batching writer. Tuned so a single request never blocks
// on the DB: events are buffered and flushed by a background worker.
const (
	defaultBatchSize     = 100
	defaultFlushInterval = 2 * time.Second
	defaultQueueSize     = 4096
)

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

	closeOnce sync.Once
	done      chan struct{}
}

// NewPostgresSink connects to the given DSN, verifies connectivity, and starts
// the background flush worker. Call Close to drain and shut down. The caller is
// responsible for having applied migrations/001_usage_events.sql beforehand.
func NewPostgresSink(ctx context.Context, dsn string, log *zap.Logger) (*PostgresSink, error) {
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
		pool:          pool,
		log:           log,
		queue:         make(chan usage.Event, defaultQueueSize),
		batchSize:     defaultBatchSize,
		flushInterval: defaultFlushInterval,
		done:          make(chan struct{}),
	}
	s.wg.Add(1)
	go s.run()
	return s, nil
}

// Record implements usage.Sink. It enqueues the event without blocking; if the
// buffer is full (DB slow/down) the event is dropped with a warning rather than
// stalling the request path — metering must never degrade the gateway.
func (s *PostgresSink) Record(_ context.Context, e usage.Event) {
	select {
	case s.queue <- e:
	default:
		if s.log != nil {
			s.log.Warn("billing: usage queue full, dropping event",
				zap.String("tenant", e.Tenant),
				zap.String("request_id", e.RequestID),
			)
		}
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

// insertSQL is standard PostgreSQL — no Timescale-specific syntax.
const insertSQL = `INSERT INTO usage_events
	(ts, tenant, subject, request_id, route_id, model,
	 prompt_tokens, completion_tokens, total_tokens, streamed, success, duration_ms)
	VALUES (now(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

// writeBatch persists a slice of events in one round-trip using pgx.Batch.
func (s *PostgresSink) writeBatch(events []usage.Event) {
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
			if s.log != nil {
				s.log.Error("billing: batch insert failed", zap.Error(err), zap.Int("batch_size", len(events)))
			}
			return
		}
	}
}

// Close stops the worker, drains the queue with a final flush, and closes the
// connection pool. Safe to call once; subsequent calls are no-ops.
func (s *PostgresSink) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.wg.Wait()
		s.pool.Close()
	})
}
