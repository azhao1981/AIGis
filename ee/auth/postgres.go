// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// hashKey returns the SHA-256 hex digest of a raw API key. Only the digest is
// ever stored or compared, so the plaintext key never touches the database.
func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// PostgresAPIKeyProvider authenticates via API keys stored in PostgreSQL. Keys
// are loaded into an in-memory snapshot at startup (and on Reload), so the
// per-request path never hits the DB — the same low-latency profile as the
// static provider, but centrally managed and revocable.
type PostgresAPIKeyProvider struct {
	pool *pgxpool.Pool
	log  *zap.Logger

	mu   sync.RWMutex
	keys map[string]Principal // key_hash -> principal

	// Background refresh: keeps this replica's snapshot in sync with keys
	// created/revoked on other replicas. Started by StartRefresh, stopped by Close.
	wg        sync.WaitGroup
	done      chan struct{}
	closeOnce sync.Once

	// refreshFn is the action run on each refresh tick (and on a pub/sub
	// notification). Defaults to Reload; tests swap it to observe refreshes
	// without a real database.
	refreshFn func(context.Context) error

	// pub is an optional Redis client used to broadcast a key-change event to
	// other replicas after a local CreateKey/RevokeKey, so they refresh within
	// a second instead of waiting for the next poll tick. nil = broadcast off
	// (polling still keeps replicas eventually consistent). Set via SetPublisher.
	pub *redis.Client
}

// NewPostgresAPIKeyProvider connects, verifies connectivity, and loads the
// current key set. Call Close when done.
func NewPostgresAPIKeyProvider(ctx context.Context, dsn string, log *zap.Logger) (*PostgresAPIKeyProvider, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("auth: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("auth: ping: %w", err)
	}
	p := &PostgresAPIKeyProvider{pool: pool, log: log, keys: map[string]Principal{}, done: make(chan struct{})}
	if err := p.Reload(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

// StartRefresh launches a background goroutine that reloads the key snapshot
// from the DB every interval, so keys created/revoked on other replicas take
// effect here within one interval (multi-replica consistency). interval <= 0
// disables it (single-replica). Idempotent effect via Close's guard.
func (p *PostgresAPIKeyProvider) StartRefresh(interval time.Duration) {
	if interval <= 0 {
		return
	}
	refresh := p.refreshFn
	if refresh == nil {
		refresh = p.Reload
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := refresh(ctx); err != nil && p.log != nil {
					p.log.Warn("auth: background key reload failed", zap.Error(err))
				}
				cancel()
			case <-p.done:
				return
			}
		}
	}()
}

// Reload replaces the in-memory snapshot with the current enabled keys from the
// DB. Safe to call concurrently with Authenticate.
func (p *PostgresAPIKeyProvider) Reload(ctx context.Context) error {
	rows, err := p.pool.Query(ctx,
		`SELECT key_hash, tenant, subject, is_admin FROM api_keys WHERE enabled = TRUE`)
	if err != nil {
		return fmt.Errorf("auth: load keys: %w", err)
	}
	defer rows.Close()

	next := map[string]Principal{}
	for rows.Next() {
		var hash, tenant, subject string
		var admin bool
		if err := rows.Scan(&hash, &tenant, &subject, &admin); err != nil {
			return fmt.Errorf("auth: scan key: %w", err)
		}
		next[hash] = Principal{Tenant: tenant, Subject: subject, Admin: admin}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	p.mu.Lock()
	p.keys = next
	p.mu.Unlock()
	if p.log != nil {
		p.log.Info("auth: API keys loaded from DB", zap.Int("count", len(next)))
	}
	return nil
}

// Authenticate resolves the request's Bearer token to a tenant using the
// in-memory snapshot. Unknown or missing keys are rejected.
func (p *PostgresAPIKeyProvider) Authenticate(r *http.Request) (Principal, error) {
	token := bearerToken(r)
	if token == "" {
		return Principal{}, ErrUnauthorized
	}
	p.mu.RLock()
	principal, ok := p.keys[hashKey(token)]
	p.mu.RUnlock()
	if !ok {
		return Principal{}, ErrUnauthorized
	}
	if principal.Subject == "" {
		principal.Subject = token
	}
	return principal, nil
}

// Actor identifies the admin principal that made a key change, recorded in the
// audit trail. A zero Actor (e.g. system bootstrap) is fine; use "bootstrap".
type Actor struct {
	Subject string
	Tenant  string
}

// CreateKey inserts a new API key (storing only its hash) and refreshes the
// snapshot so it is immediately usable. admin grants /admin/* privileges. by
// records who made the change in the audit trail.
func (p *PostgresAPIKeyProvider) CreateKey(ctx context.Context, rawKey, tenant, subject string, admin bool, by Actor) error {
	if rawKey == "" || tenant == "" {
		return fmt.Errorf("auth: key and tenant are required")
	}
	_, err := p.pool.Exec(ctx,
		`INSERT INTO api_keys (key_hash, tenant, subject, is_admin) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (key_hash) DO UPDATE SET tenant = EXCLUDED.tenant,
		     subject = EXCLUDED.subject, is_admin = EXCLUDED.is_admin, enabled = TRUE`,
		hashKey(rawKey), tenant, subject, admin)
	if err != nil {
		return fmt.Errorf("auth: create key: %w", err)
	}
	p.writeAudit(ctx, "create", hashKey(rawKey), tenant, admin, by)
	if err := p.Reload(ctx); err != nil {
		return err
	}
	p.publish(ctx, "create")
	return nil
}

// RevokeKey soft-disables a key by its raw value and refreshes the snapshot. by
// records who made the change in the audit trail. The revoked key's tenant is
// captured in the same statement so the audit row (and tenant-scoped queries)
// know which tenant was affected.
func (p *PostgresAPIKeyProvider) RevokeKey(ctx context.Context, rawKey string, by Actor) error {
	var tenant string
	err := p.pool.QueryRow(ctx,
		`UPDATE api_keys SET enabled = FALSE WHERE key_hash = $1 RETURNING tenant`,
		hashKey(rawKey)).Scan(&tenant)
	if err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("auth: revoke key: key not found")
		}
		return fmt.Errorf("auth: revoke key: %w", err)
	}
	p.writeAudit(ctx, "revoke", hashKey(rawKey), tenant, false, by)
	if err := p.Reload(ctx); err != nil {
		return err
	}
	p.publish(ctx, "revoke")
	return nil
}

// KeyTenant returns the tenant a raw key belongs to, or ("", false) if the key is
// unknown. It lets the admin layer enforce that a tenant administrator only acts
// on keys within their own tenant before calling RevokeKey.
func (p *PostgresAPIKeyProvider) KeyTenant(ctx context.Context, rawKey string) (string, bool) {
	var tenant string
	err := p.pool.QueryRow(ctx,
		`SELECT tenant FROM api_keys WHERE key_hash = $1`, hashKey(rawKey)).Scan(&tenant)
	if err != nil {
		return "", false
	}
	return tenant, true
}

// writeAudit records a key change. Only the hash is stored (never plaintext).
// A failed audit write does NOT roll back the key change (already committed) —
// it is logged loudly instead (fail-loud), so a broken audit table is visible
// without silently blocking key management.
func (p *PostgresAPIKeyProvider) writeAudit(ctx context.Context, action, keyHash, targetTenant string, targetAdmin bool, by Actor) {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO api_key_audit (action, key_hash, target_tenant, target_admin, actor_subject, actor_tenant)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		action, keyHash, targetTenant, targetAdmin, by.Subject, by.Tenant)
	if err != nil && p.log != nil {
		p.log.Error("auth: key audit write failed (key change already applied)",
			zap.String("action", action), zap.Error(err))
	}
}

// AuditFilter narrows an audit query. Zero fields mean "no filter"; Limit <= 0
// falls back to a sane default; Offset pages the result. TargetTenant scopes the
// trail to key changes affecting one tenant (used to isolate tenant admins).
type AuditFilter struct {
	KeyHash      string
	Action       string
	TargetTenant string
	Limit        int
	Offset       int
}

// AuditRow is one recorded key change (hash only, no secret material).
type AuditRow struct {
	TS           time.Time `json:"ts"`
	Action       string    `json:"action"`
	KeyHash      string    `json:"key_hash"`
	TargetTenant string    `json:"target_tenant"`
	TargetAdmin  bool      `json:"target_admin"`
	ActorSubject string    `json:"actor_subject"`
	ActorTenant  string    `json:"actor_tenant"`
}

// ListAudit returns key-change audit rows, most recent first, optionally
// filtered by key hash and/or action.
func (p *PostgresAPIKeyProvider) ListAudit(ctx context.Context, f AuditFilter) ([]AuditRow, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := p.pool.Query(ctx,
		`SELECT ts, action, key_hash, target_tenant, target_admin, actor_subject, actor_tenant
		 FROM api_key_audit
		 WHERE ($1 = '' OR key_hash = $1) AND ($2 = '' OR action = $2)
		   AND ($3 = '' OR target_tenant = $3)
		 ORDER BY ts DESC LIMIT $4 OFFSET $5`,
		f.KeyHash, f.Action, f.TargetTenant, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("auth: list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var a AuditRow
		if err := rows.Scan(&a.TS, &a.Action, &a.KeyHash, &a.TargetTenant,
			&a.TargetAdmin, &a.ActorSubject, &a.ActorTenant); err != nil {
			return nil, fmt.Errorf("auth: scan audit: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AuditKeyHash exposes the hash used to correlate a raw key with audit rows,
// so callers (e.g. the admin endpoint) can filter by a plaintext key without
// re-deriving the digest.
func AuditKeyHash(rawKey string) string { return hashKey(rawKey) }

// KeyInfo is one row of the key registry (no secret material).
type KeyInfo struct {
	Tenant  string `json:"tenant"`
	Subject string `json:"subject"`
	Enabled bool   `json:"enabled"`
	Admin   bool   `json:"admin"`
}

// KeyQuery narrows/pages a ListKeys call. Tenant is optional (empty = all);
// Limit <= 0 falls back to a default; Offset skips that many rows for paging.
type KeyQuery struct {
	Tenant string
	Limit  int
	Offset int
}

// ListKeys returns the registered keys' metadata (never the hashes/secrets),
// optionally filtered by tenant and paged.
func (p *PostgresAPIKeyProvider) ListKeys(ctx context.Context, q KeyQuery) ([]KeyInfo, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := p.pool.Query(ctx,
		`SELECT tenant, subject, enabled, is_admin FROM api_keys
		 WHERE ($1 = '' OR tenant = $1)
		 ORDER BY tenant, subject LIMIT $2 OFFSET $3`,
		q.Tenant, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("auth: list keys: %w", err)
	}
	defer rows.Close()
	var out []KeyInfo
	for rows.Next() {
		var k KeyInfo
		if err := rows.Scan(&k.Tenant, &k.Subject, &k.Enabled, &k.Admin); err != nil {
			return nil, fmt.Errorf("auth: scan key info: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Close stops the background refresh (if running) and releases the connection
// pool. Safe to call once; subsequent calls are no-ops.
func (p *PostgresAPIKeyProvider) Close() {
	p.closeOnce.Do(func() {
		close(p.done)
		p.wg.Wait()
		if p.pub != nil {
			_ = p.pub.Close()
		}
		p.pool.Close()
	})
}
