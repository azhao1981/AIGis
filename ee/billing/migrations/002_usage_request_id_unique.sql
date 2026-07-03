-- AIGis Enterprise Edition — WAL replay idempotency index.
-- Licensed under the AIGis Enterprise Edition License (see ee/LICENSE), NOT the
-- AGPLv3 that governs the open-source core.
--
-- PURPOSE: make usage_events inserts idempotent on (request_id, ts) so that the
-- WAL replay path (see ee/billing PostgresSink.replayOnce) can re-insert a spooled
-- event WITHOUT double-counting. Both the live insert and its later replay carry
-- the SAME ts (replay preserves the original metering time), so they collide on
-- this unique index and ON CONFLICT DO NOTHING drops the duplicate.
--
-- WHY (request_id, ts) AND NOT request_id ALONE:
--   usage_events is a Timescale hypertable partitioned by ts. Timescale requires
--   every UNIQUE index to include the partitioning column (ts). Hence the key is
--   the pair. This is also correct semantically: a request_id identifies one
--   request, whose single usage event has one fixed metering ts.
--
-- WHY PARTIAL (request_id <> ''):
--   Events without a request_id (empty string) cannot be deduplicated meaningfully
--   and must never collide with each other. Excluding them keeps pre-WAL behavior
--   (empty-request_id rows are always inserted) while still protecting the common
--   case where a request_id is present.
--
-- PORTABILITY: 100% standard PostgreSQL. On vanilla PG (no hypertable) the same
-- index works; the ts column in the key is simply extra precision, not required.

CREATE UNIQUE INDEX IF NOT EXISTS uq_usage_events_request_id_ts
    ON usage_events (request_id, ts)
    WHERE request_id <> '';
