// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// sessionPrefix namespaces dashboard login sessions in Redis.
const sessionPrefix = "aigis:session:"

// DefaultSessionTTL is how long a login session stays valid without activity.
const DefaultSessionTTL = 24 * time.Hour

// SessionStore holds server-side dashboard login sessions in Redis, so a login
// can be revoked instantly (logout) and sessions are shared across replicas —
// the reason we chose server-side sessions over stateless JWTs. It reuses the
// same go-redis client type as the quota limiter.
type SessionStore struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewSessionStore builds a session store from a Redis address, verifying
// connectivity. ttl <= 0 falls back to DefaultSessionTTL.
func NewSessionStore(ctx context.Context, addr, password string, db int, ttl time.Duration) (*SessionStore, error) {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("session: redis ping: %w", err)
	}
	return &SessionStore{rdb: rdb, ttl: ttl}, nil
}

// New creates a session for the authenticated principal and returns an opaque
// random session id (128 bits of entropy) to hand back as a cookie. The stored
// value is the JSON-encoded Principal, expiring after the store's TTL.
func (s *SessionStore) New(ctx context.Context, p Principal) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("session: gen id: %w", err)
	}
	id := hex.EncodeToString(buf)
	data, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("session: encode: %w", err)
	}
	if err := s.rdb.Set(ctx, sessionPrefix+id, data, s.ttl).Err(); err != nil {
		return "", fmt.Errorf("session: store: %w", err)
	}
	return id, nil
}

// Get resolves a session id back to its Principal, refreshing the TTL so active
// sessions slide forward. Returns ok=false for unknown/expired ids.
func (s *SessionStore) Get(ctx context.Context, id string) (Principal, bool) {
	if id == "" {
		return Principal{}, false
	}
	data, err := s.rdb.Get(ctx, sessionPrefix+id).Bytes()
	if err != nil {
		return Principal{}, false // redis.Nil (missing) or any error -> not authenticated
	}
	var p Principal
	if err := json.Unmarshal(data, &p); err != nil {
		return Principal{}, false
	}
	// Sliding expiration: keep active sessions alive.
	s.rdb.Expire(ctx, sessionPrefix+id, s.ttl)
	return p, true
}

// Delete invalidates a session id immediately (logout). Deleting a missing id
// is a no-op.
func (s *SessionStore) Delete(ctx context.Context, id string) {
	if id == "" {
		return
	}
	s.rdb.Del(ctx, sessionPrefix+id)
}

// Close releases the Redis connection.
func (s *SessionStore) Close() error {
	return s.rdb.Close()
}
