// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"context"
	"encoding/json"
	"net/http"

	"aigis/internal/server"
)

// skipPaths are unauthenticated endpoints (health/liveness), so probes and load
// balancers don't need a token.
var skipPaths = map[string]bool{
	"/health": true,
	"/":       true,
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

			ctx := context.WithValue(r.Context(), tenantKey{}, principal)
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
