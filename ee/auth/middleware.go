// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"context"
	"encoding/json"
	"net/http"

	"aigis/internal/core"
	"aigis/internal/server"
)

// skipPaths are unauthenticated endpoints, so probes/load balancers and the
// admin dashboard's initial load don't need a token. /ui serves the static page
// and /ui/capabilities advertises which panels to show — both must be reachable
// before the operator has entered a token; the /admin/* data behind the panels
// stays auth-guarded.
var skipPaths = map[string]bool{
	"/health":          true,
	"/":                true,
	"/ui":              true,
	"/ui/capabilities": true,
}

// Middleware returns a server.Middleware that authenticates every gateway
// request via the given AuthProvider and stashes the resolved Principal in the
// request context (retrievable with FromContext). Requests to skipPaths bypass
// auth. This is the sole seam between the EE auth layer and the OSS core.
func Middleware(p AuthProvider) server.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skipPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			principal, err := p.Authenticate(r)
			if err != nil {
				writeAuthError(w, err)
				return
			}

			// Stash under the ee-local key (for FromContext) and, crucially,
			// under the core key so the OSS gateway handler can scope logging,
			// audit, and quota per tenant without importing ee.
			ctx := context.WithValue(r.Context(), tenantKey{}, principal)
			ctx = core.WithTenant(ctx, core.TenantIdentity{Tenant: principal.Tenant, Subject: principal.Subject})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// writeAuthError renders a 401 in an OpenAI-compatible error envelope so clients
// parse it uniformly.
func writeAuthError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"type":    "authentication_error",
			"message": err.Error(),
		},
	})
}
