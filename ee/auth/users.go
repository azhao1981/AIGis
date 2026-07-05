// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidCredentials is returned when an email/password pair does not match
// an enabled user. It is intentionally vague (never says which of the two was
// wrong) so it can't be used to probe which emails exist.
var ErrInvalidCredentials = errors.New("invalid email or password")

// ErrEmailTaken is returned by RegisterUser when the email already belongs to a
// user. Unlike CreateUser's idempotent upsert, self-service registration must
// refuse to touch an existing account (no password/tenant/privilege overwrite).
var ErrEmailTaken = errors.New("email already registered")

// UserStore manages human dashboard logins in PostgreSQL: SaaS operators who log
// into the admin UI, as opposed to the machine-facing API keys. Passwords are
// stored only as bcrypt hashes (plaintext never touches the DB). A user belongs
// to one tenant (tenant == user group in this model).
type UserStore struct {
	pool *pgxpool.Pool
	log  *zap.Logger
}

// NewUserStore connects, verifies connectivity, and returns a store. It shares
// the same DSN as the api-key/usage stores. Call Close when done.
func NewUserStore(ctx context.Context, dsn string, log *zap.Logger) (*UserStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("users: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("users: ping: %w", err)
	}
	return &UserStore{pool: pool, log: log}, nil
}

// Close releases the connection pool.
func (s *UserStore) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// normalizeEmail lower-cases and trims the email so logins are case-insensitive
// and whitespace-tolerant, matching how the UNIQUE constraint should behave.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// CreateUser inserts (or updates) a dashboard user, storing only the bcrypt hash
// of the password. It is an idempotent upsert on email so bootstrap can re-run
// safely on every restart. admin grants /admin/* privileges.
func (s *UserStore) CreateUser(ctx context.Context, email, password, tenant string, admin bool) error {
	email = normalizeEmail(email)
	if email == "" || password == "" || tenant == "" {
		return fmt.Errorf("users: email, password and tenant are required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("users: hash password: %w", err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO users (email, password_hash, tenant, is_admin) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (email) DO UPDATE SET password_hash = EXCLUDED.password_hash,
		     tenant = EXCLUDED.tenant, is_admin = EXCLUDED.is_admin, enabled = TRUE`,
		email, string(hash), tenant, admin)
	if err != nil {
		return fmt.Errorf("users: create user: %w", err)
	}
	return nil
}

// RegisterUser is the self-service signup path (POST /register). Unlike
// CreateUser it never overwrites an existing account: an INSERT ... ON CONFLICT
// DO NOTHING returns ErrEmailTaken instead, so a stranger cannot hijack another
// user's password/tenant/privileges by re-registering their email. is_admin is
// hard-coded FALSE — self-registered users are always ordinary members; admin
// grants come only from bootstrap config or a platform admin. The account is
// created enabled (immediately usable) — this is the path taken when email
// verification is off.
func (s *UserStore) RegisterUser(ctx context.Context, email, password, tenant string) error {
	return s.registerUser(ctx, email, password, tenant, true)
}

// RegisterUserPending is the signup path when email verification is ON: it
// creates the account disabled (enabled=FALSE) so the user cannot log in until
// they click the verification link (ActivateUser flips enabled=TRUE). Same
// no-overwrite / non-admin guarantees as RegisterUser.
func (s *UserStore) RegisterUserPending(ctx context.Context, email, password, tenant string) error {
	return s.registerUser(ctx, email, password, tenant, false)
}

// registerUser is the shared self-service INSERT: non-admin, no-overwrite
// (ON CONFLICT DO NOTHING -> ErrEmailTaken), with enabled controlling whether
// the account is immediately usable or awaits email verification.
func (s *UserStore) registerUser(ctx context.Context, email, password, tenant string, enabled bool) error {
	email = normalizeEmail(email)
	if email == "" || password == "" || tenant == "" {
		return fmt.Errorf("users: email, password and tenant are required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("users: hash password: %w", err)
	}
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO users (email, password_hash, tenant, is_admin, enabled) VALUES ($1, $2, $3, FALSE, $4)
		 ON CONFLICT (email) DO NOTHING`,
		email, string(hash), tenant, enabled)
	if err != nil {
		return fmt.Errorf("users: register user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrEmailTaken
	}
	return nil
}

// VerifyPassword checks an email/password pair against an enabled user and, on
// success, returns the Principal (tenant/subject/admin) to seat in a session.
// Any mismatch (unknown email, disabled user, wrong password) returns
// ErrInvalidCredentials — the same error, so callers can't distinguish causes.
func (s *UserStore) VerifyPassword(ctx context.Context, email, password string) (Principal, error) {
	email = normalizeEmail(email)
	var hash, tenant string
	var admin bool
	err := s.pool.QueryRow(ctx,
		`SELECT password_hash, tenant, is_admin FROM users WHERE email = $1 AND enabled = TRUE`,
		email).Scan(&hash, &tenant, &admin)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrInvalidCredentials
	}
	if err != nil {
		return Principal{}, fmt.Errorf("users: lookup: %w", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return Principal{}, ErrInvalidCredentials
	}
	return Principal{Tenant: tenant, Subject: email, Admin: admin}, nil
}
