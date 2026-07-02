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

	"github.com/jackc/pgx/v5/pgxpool"
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
	p := &PostgresAPIKeyProvider{pool: pool, log: log, keys: map[string]Principal{}}
	if err := p.Reload(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
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

// CreateKey inserts a new API key (storing only its hash) and refreshes the
// snapshot so it is immediately usable. admin grants /admin/* privileges.
func (p *PostgresAPIKeyProvider) CreateKey(ctx context.Context, rawKey, tenant, subject string, admin bool) error {
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
	return p.Reload(ctx)
}

// RevokeKey soft-disables a key by its raw value and refreshes the snapshot.
func (p *PostgresAPIKeyProvider) RevokeKey(ctx context.Context, rawKey string) error {
	if _, err := p.pool.Exec(ctx,
		`UPDATE api_keys SET enabled = FALSE WHERE key_hash = $1`, hashKey(rawKey)); err != nil {
		return fmt.Errorf("auth: revoke key: %w", err)
	}
	return p.Reload(ctx)
}

// KeyInfo is one row of the key registry (no secret material).
type KeyInfo struct {
	Tenant  string `json:"tenant"`
	Subject string `json:"subject"`
	Enabled bool   `json:"enabled"`
	Admin   bool   `json:"admin"`
}

// ListKeys returns the registered keys' metadata (never the hashes/secrets).
func (p *PostgresAPIKeyProvider) ListKeys(ctx context.Context) ([]KeyInfo, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT tenant, subject, enabled, is_admin FROM api_keys ORDER BY tenant, subject`)
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

// Close releases the connection pool.
func (p *PostgresAPIKeyProvider) Close() {
	p.pool.Close()
}
