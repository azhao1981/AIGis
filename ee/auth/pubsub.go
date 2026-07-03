// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// keyChangeChannel is the Redis pub/sub channel over which replicas announce a
// key create/revoke so peers refresh their snapshot within a second, rather than
// waiting for the next StartRefresh poll tick. The payload is just the action
// string ("create"/"revoke") — the receiver always reloads the full snapshot, so
// the message content is only a hint, never trusted data.
const keyChangeChannel = "aigis:auth:keychange"

// NewKeyChangeRedis builds a Redis client for the key-change pub/sub channel,
// verifying connectivity. It mirrors NewSessionStore's client setup so a single
// Redis (the quota/session one) can also carry key-change events.
func NewKeyChangeRedis(ctx context.Context, addr, password string, db int) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("auth: keychange redis ping: %w", err)
	}
	return rdb, nil
}

// SetPublisher enables broadcasting of key changes on this provider: after a
// local CreateKey/RevokeKey the provider publishes a notice on keyChangeChannel
// so other replicas refresh immediately. Passing nil disables broadcasting.
func (p *PostgresAPIKeyProvider) SetPublisher(rdb *redis.Client) {
	p.pub = rdb
}

// publish broadcasts a key-change notice to other replicas. It is best-effort:
// a publish failure never rolls back the (already committed) key change — it is
// logged loudly (fail-loud) and the polling refresh remains the safety net. A
// nil publisher (broadcast disabled) is a no-op.
func (p *PostgresAPIKeyProvider) publish(ctx context.Context, action string) {
	if p.pub == nil {
		return
	}
	if err := p.pub.Publish(ctx, keyChangeChannel, action).Err(); err != nil && p.log != nil {
		p.log.Warn("auth: key-change publish failed (change already applied; polling will still converge)",
			zap.String("action", action), zap.Error(err))
	}
}

// StartSubscribe launches a background goroutine that subscribes to the
// key-change channel and refreshes the snapshot on every notice, so a key
// revoked/created on another replica takes effect here within a second. It runs
// alongside StartRefresh (pub/sub for speed, polling as the fail-safe if a
// message is missed or Redis briefly drops). go-redis's Channel() reconnects
// internally, so a transient Redis blip self-heals. Stopped by Close.
func (p *PostgresAPIKeyProvider) StartSubscribe(ctx context.Context, rdb *redis.Client) {
	if rdb == nil {
		return
	}
	refresh := p.refreshFn
	if refresh == nil {
		refresh = p.Reload
	}
	sub := rdb.Subscribe(ctx, keyChangeChannel)
	ch := sub.Channel()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer sub.Close()
		for {
			select {
			case <-ch:
				rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := refresh(rctx); err != nil && p.log != nil {
					p.log.Warn("auth: key reload on pub/sub notice failed", zap.Error(err))
				}
				cancel()
			case <-p.done:
				return
			}
		}
	}()
	if p.log != nil {
		p.log.Info("auth: key-change pub/sub subscribed", zap.String("channel", keyChangeChannel))
	}
}
