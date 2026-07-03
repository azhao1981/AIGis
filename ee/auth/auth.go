// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

// Package auth is the Enterprise Edition inbound authentication and multi-tenant
// layer. It plugs into the open-source core purely as an http middleware
// (server.Middleware), so the core never imports this package — the dependency
// is one-way (ee -> core).
package auth

import (
	"context"
	"net/http"
	"strings"
)

// tenantKey is the context key under which the resolved tenant is stored.
type tenantKey struct{}

// Principal is the authenticated caller resolved from a request: which tenant
// they belong to and the key/subject they authenticated as.
type Principal struct {
	Tenant  string
	Subject string
	// Admin marks a privileged caller allowed to use the /admin/* management
	// endpoints. Ordinary tenant keys have this false and are limited to the
	// gateway.
	Admin bool
}

// AuthProvider resolves a request into a Principal. Implementations decide the
// scheme (static API keys, OIDC/JWT, mTLS, ...). Returning an error denies the
// request. This is the core Enterprise extension point.
type AuthProvider interface {
	Authenticate(r *http.Request) (Principal, error)
}

// FromContext returns the Principal stored by the auth middleware, if any.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(tenantKey{}).(Principal)
	return p, ok
}

// IsAdmin reports whether the request's authenticated caller holds admin
// privileges (allowed to use /admin/* endpoints). A request with no resolved
// principal is not an admin.
func IsAdmin(ctx context.Context) bool {
	p, ok := FromContext(ctx)
	return ok && p.Admin
}

// EffectiveTenant computes the tenant scope an admin request is confined to, for
// multi-tenant data isolation on the /admin/* endpoints. An admin belonging to
// the platformTenant is a platform administrator and sees every tenant, so scope
// is "" (no filter) and isPlatform is true. Any other admin is a tenant
// administrator, pinned to their own tenant regardless of what they ask for in
// the query string. A request with no principal yields ("", false) — the caller
// still guards with IsAdmin, so this is only reached for admins.
func EffectiveTenant(ctx context.Context, platformTenant string) (scope string, isPlatform bool) {
	p, ok := FromContext(ctx)
	if !ok {
		return "", false
	}
	if p.Tenant == platformTenant {
		return "", true
	}
	return p.Tenant, false
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header,
// or returns "" if absent/malformed.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
