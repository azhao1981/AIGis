// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"context"
	"net/http"

	"go.uber.org/zap"

	"aigis/internal/server"
)

// emailUserAPI is the narrow slice of *UserStore the email flows need. Declaring
// it here (rather than widening the existing userAPI) keeps the C1 session
// middleware and its tests untouched. An in-memory fake satisfies it in tests.
type emailUserAPI interface {
	RegisterUserPending(ctx context.Context, email, password, tenant string) error
	IssueToken(ctx context.Context, email, purpose string) (string, error)
	ConsumeToken(ctx context.Context, token, purpose string) (string, error)
	ActivateUser(ctx context.Context, email string) error
	UpdatePassword(ctx context.Context, email, newPassword string) error
	SetUserAdmin(ctx context.Context, email, tenantScope string, admin bool) error
}

// EmailOptions configures the email-driven account flows (verification, reset,
// role management). It is wired in serve.go only when a Mailer is enabled.
type EmailOptions struct {
	// Verify turns on email verification for self-service signup: /register
	// creates a disabled account and mails a verification link instead of
	// activating immediately. When false, /register is left entirely to the
	// session middleware (C1 behavior).
	Verify bool
	// BaseURL is the externally reachable dashboard origin used to build links
	// in emails (e.g. https://app.example.com). "/verify?token=..." is appended.
	BaseURL string
	// PlatformTenant names the tenant whose admins manage roles across tenants;
	// any other admin is confined to their own tenant (same rule as elsewhere).
	PlatformTenant string
}

// EmailMiddleware adds the SMTP-backed PUBLIC (unauthenticated) account flows as
// a standalone seam, sitting IN FRONT OF SessionMiddleware in the chain so it
// can intercept /register when verification is on. Routes it owns:
//
//   - POST /register  (only when Verify=true) create disabled + mail a link
//   - GET  /verify?token=   activate the account behind a verify token
//   - POST /forgot    {email}           mail a reset link (always 200, no probe)
//   - POST /reset     {token,password}  set a new password behind a reset token
//
// These are all unauthenticated (verified by the token, not a session), so this
// seam runs before auth. Role management (which needs an authenticated admin)
// lives in RoleMiddleware, installed downstream. Every other path passes
// straight through. When the Mailer is disabled this middleware is not installed
// at all, so these routes simply don't exist.
func EmailMiddleware(users emailUserAPI, mailer Mailer, opts EmailOptions, log *zap.Logger) server.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/register":
				// Only take over signup when verification is on; otherwise let
				// the session middleware handle it (immediate activation).
				if !opts.Verify {
					next.ServeHTTP(w, r)
					return
				}
				handleRegisterVerify(w, r, users, mailer, opts, log)
				return
			case "/verify":
				handleVerify(w, r, users, log)
				return
			case "/forgot":
				handleForgot(w, r, users, mailer, opts, log)
				return
			case "/reset":
				handleReset(w, r, users, log)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RoleMiddleware serves POST /admin/users/role (promote/demote a user within the
// caller's tenant scope) and passes everything else through. It must be
// installed DOWNSTREAM of the authentication middleware so the admin Principal
// is present in the context (IsAdmin / EffectiveTenant) — same placement as
// AdminMiddleware for /admin/keys.
func RoleMiddleware(users emailUserAPI, opts EmailOptions, log *zap.Logger) server.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/admin/users/role" {
				next.ServeHTTP(w, r)
				return
			}
			handleSetRole(w, r, users, opts, log)
		})
	}
}
