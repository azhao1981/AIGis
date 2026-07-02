// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"context"
	"encoding/json"
	"net/http"

	"go.uber.org/zap"

	"aigis/internal/core"
	"aigis/internal/server"
)

// sessionCookie is the name of the cookie carrying the dashboard session id.
const sessionCookie = "aigis_session"

// sessions abstracts the session store so the middleware can be unit-tested with
// an in-memory fake (no Redis). *SessionStore satisfies it.
type sessionAPI interface {
	New(ctx context.Context, p Principal) (string, error)
	Get(ctx context.Context, id string) (Principal, bool)
	Delete(ctx context.Context, id string)
}

// userAPI abstracts credential verification for testing. *UserStore satisfies it.
type userAPI interface {
	VerifyPassword(ctx context.Context, email, password string) (Principal, error)
}

// SessionMiddleware authenticates dashboard (human) and gateway (machine) callers
// in one chain, for the SaaS deployment:
//
//   - POST /login       email+password -> creates a session, sets the cookie
//   - POST /logout      invalidates the session and clears the cookie
//   - GET  /me          returns the current principal (who am I)
//   - everything else   authenticated by session cookie first, then Bearer API
//     key (so programmatic clients keep working unchanged)
//
// It supersedes Middleware when a UserStore + SessionStore are configured, and
// like every EE seam it plugs in purely as a server.Middleware (the core never
// imports this package).
func SessionMiddleware(users userAPI, sessions sessionAPI, keys AuthProvider, log *zap.Logger) server.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/login":
				handleLogin(w, r, users, sessions, log)
				return
			case "/logout":
				handleLogout(w, r, sessions)
				return
			case "/me":
				handleMe(w, r, sessions)
				return
			}

			if skipPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			// Prefer a dashboard session cookie; fall back to a Bearer API key so
			// programmatic gateway clients are unaffected.
			principal, ok := principalFromSession(r, sessions)
			if !ok {
				p, err := keys.Authenticate(r)
				if err != nil {
					writeAuthError(w, err)
					return
				}
				principal = p
			}

			ctx := context.WithValue(r.Context(), tenantKey{}, principal)
			ctx = core.WithTenant(ctx, core.TenantIdentity{Tenant: principal.Tenant, Subject: principal.Subject})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// principalFromSession resolves the session cookie to a Principal, if present
// and valid.
func principalFromSession(r *http.Request, sessions sessionAPI) (Principal, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Principal{}, false
	}
	return sessions.Get(r.Context(), c.Value)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleLogin verifies credentials, creates a session, and sets an HttpOnly
// cookie. Only POST is accepted.
func handleLogin(w http.ResponseWriter, r *http.Request, users userAPI, sessions sessionAPI, log *zap.Logger) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	principal, err := users.VerifyPassword(r.Context(), req.Email, req.Password)
	if err != nil {
		// Invalid credentials -> 401; anything else is a server error.
		if err == ErrInvalidCredentials {
			writeJSONError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if log != nil {
			log.Warn("login: verify failed", zap.Error(err))
		}
		writeJSONError(w, http.StatusInternalServerError, "login failed")
		return
	}
	id, err := sessions.New(r.Context(), principal)
	if err != nil {
		if log != nil {
			log.Error("login: create session failed", zap.Error(err))
		}
		writeJSONError(w, http.StatusInternalServerError, "login failed")
		return
	}
	setSessionCookie(w, id)
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant": principal.Tenant, "subject": principal.Subject, "admin": principal.Admin,
	})
}

// handleLogout invalidates the session server-side and clears the cookie.
func handleLogout(w http.ResponseWriter, r *http.Request, sessions sessionAPI) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		sessions.Delete(r.Context(), c.Value)
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleMe reports the currently logged-in principal, or 401 if there is no
// valid session.
func handleMe(w http.ResponseWriter, r *http.Request, sessions sessionAPI) {
	principal, ok := principalFromSession(r, sessions)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant": principal.Tenant, "subject": principal.Subject, "admin": principal.Admin,
	})
}

// setSessionCookie writes the HttpOnly session cookie. SameSite=Lax and path=/
// keep it scoped to same-site dashboard navigation; Secure is left off so it
// works over plain HTTP in dev — a TLS-terminating proxy in production should
// enforce HTTPS.
func setSessionCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]string{"type": "authentication_error", "message": msg},
	})
}
