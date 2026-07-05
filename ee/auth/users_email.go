// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

// Email-token purposes. A single email_tokens table serves both flows,
// distinguished by this column so a verify token can't be replayed as a reset.
const (
	PurposeVerify = "verify"
	PurposeReset  = "reset"
)

// DefaultTokenTTL bounds how long a verification / reset link stays valid.
const DefaultTokenTTL = 2 * time.Hour

// ErrTokenInvalid is returned when a token is unknown, already used, expired, or
// its purpose does not match — all collapsed into one error so a caller can't
// probe which case it was.
var ErrTokenInvalid = errors.New("invalid or expired token")

// ErrUserNotFound is returned by role/admin updates when the target email does
// not exist in the caller's scope.
var ErrUserNotFound = errors.New("user not found")

// IssueToken creates a one-time token for the given email+purpose, valid for
// DefaultTokenTTL, and returns the opaque token string (128 bits). Any earlier
// tokens for the same email+purpose are deleted first so only the newest link
// works (issuing a fresh reset supersedes older ones).
func (s *UserStore) IssueToken(ctx context.Context, email, purpose string) (string, error) {
	email = normalizeEmail(email)
	if email == "" || purpose == "" {
		return "", fmt.Errorf("users: email and purpose are required")
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("users: gen token: %w", err)
	}
	token := hex.EncodeToString(buf)
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM email_tokens WHERE email = $1 AND purpose = $2`, email, purpose); err != nil {
		return "", fmt.Errorf("users: clear old tokens: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO email_tokens (token, email, purpose, expires_at) VALUES ($1, $2, $3, $4)`,
		token, email, purpose, time.Now().Add(DefaultTokenTTL)); err != nil {
		return "", fmt.Errorf("users: store token: %w", err)
	}
	return token, nil
}

// ConsumeToken validates a token against the expected purpose and, if valid and
// unexpired, deletes it (single-use) and returns the email it was issued for.
// Any invalid/expired/mismatched token yields ErrTokenInvalid.
func (s *UserStore) ConsumeToken(ctx context.Context, token, purpose string) (string, error) {
	if token == "" {
		return "", ErrTokenInvalid
	}
	var email string
	var expires time.Time
	err := s.pool.QueryRow(ctx,
		`DELETE FROM email_tokens WHERE token = $1 AND purpose = $2 RETURNING email, expires_at`,
		token, purpose).Scan(&email, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTokenInvalid
	}
	if err != nil {
		return "", fmt.Errorf("users: consume token: %w", err)
	}
	if time.Now().After(expires) {
		return "", ErrTokenInvalid
	}
	return email, nil
}

// ActivateUser enables an account after successful email verification. It is a
// no-op (no error) if the user is already enabled or absent — activation is
// idempotent so a double-clicked link doesn't error.
func (s *UserStore) ActivateUser(ctx context.Context, email string) error {
	email = normalizeEmail(email)
	if _, err := s.pool.Exec(ctx,
		`UPDATE users SET enabled = TRUE WHERE email = $1`, email); err != nil {
		return fmt.Errorf("users: activate: %w", err)
	}
	return nil
}

// UpdatePassword replaces a user's password with a fresh bcrypt hash (used by
// the reset flow after a valid reset token). Plaintext never touches the DB.
func (s *UserStore) UpdatePassword(ctx context.Context, email, newPassword string) error {
	email = normalizeEmail(email)
	if email == "" || newPassword == "" {
		return fmt.Errorf("users: email and new password are required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("users: hash password: %w", err)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET password_hash = $2 WHERE email = $1`, email, string(hash))
	if err != nil {
		return fmt.Errorf("users: update password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

// SetUserAdmin promotes (owner/admin) or demotes (member) a user WITHIN a
// tenant. The tenant filter is the isolation boundary: a tenant admin may only
// change roles inside their own tenant, so callers pass their EffectiveTenant
// scope. A platform admin passes "" to reach any tenant. Returns ErrUserNotFound
// if no user matches email within the given scope.
func (s *UserStore) SetUserAdmin(ctx context.Context, email, tenantScope string, admin bool) error {
	email = normalizeEmail(email)
	if email == "" {
		return fmt.Errorf("users: email is required")
	}
	var tag pgconn.CommandTag
	var err error
	if tenantScope == "" {
		tag, err = s.pool.Exec(ctx,
			`UPDATE users SET is_admin = $2 WHERE email = $1`, email, admin)
	} else {
		tag, err = s.pool.Exec(ctx,
			`UPDATE users SET is_admin = $2 WHERE email = $1 AND tenant = $3`, email, admin, tenantScope)
	}
	if err != nil {
		return fmt.Errorf("users: set role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}
