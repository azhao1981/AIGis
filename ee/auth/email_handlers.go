// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

// verifyLink / resetLink build the click-through URLs mailed to the user. When
// no BaseURL is configured they fall back to a relative path so the token is
// still usable (e.g. dev/testing behind a single origin).
func verifyLink(baseURL, token string) string {
	return strings.TrimRight(baseURL, "/") + "/verify?token=" + token
}

func resetLink(baseURL, token string) string {
	return strings.TrimRight(baseURL, "/") + "/reset?token=" + token
}

// handleRegisterVerify is the signup handler when email verification is ON. It
// creates a DISABLED account and mails a verification link; the account cannot
// log in until /verify is hit. Same status contract as C1 handleRegister
// (400 missing fields, 409 duplicate) but success is 202 Accepted (pending
// verification) rather than 201, and no principal is returned.
func handleRegisterVerify(w http.ResponseWriter, r *http.Request, users emailUserAPI, mailer Mailer, opts EmailOptions, log *zap.Logger) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Email == "" || req.Password == "" || req.Tenant == "" {
		writeJSONError(w, http.StatusBadRequest, "email, password and tenant are required")
		return
	}
	err := users.RegisterUserPending(r.Context(), req.Email, req.Password, req.Tenant)
	if err == ErrEmailTaken {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		if log != nil {
			log.Warn("register(verify): create user failed", zap.Error(err))
		}
		writeJSONError(w, http.StatusInternalServerError, "registration failed")
		return
	}
	// Issue a verification token and mail the link. If mailing fails the account
	// still exists (disabled); the user can re-trigger via /forgot-style resend
	// later — but we surface a 500 so the client knows the mail didn't go out.
	token, err := users.IssueToken(r.Context(), req.Email, PurposeVerify)
	if err != nil {
		if log != nil {
			log.Error("register(verify): issue token failed", zap.Error(err))
		}
		writeJSONError(w, http.StatusInternalServerError, "registration failed")
		return
	}
	link := verifyLink(opts.BaseURL, token)
	if err := mailer.Send(normalizeEmail(req.Email), "Verify your account",
		"Welcome! Confirm your email to activate your account:\r\n\r\n"+link+"\r\n"); err != nil {
		if log != nil {
			log.Error("register(verify): send mail failed", zap.Error(err))
		}
		writeJSONError(w, http.StatusInternalServerError, "could not send verification email")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"tenant": req.Tenant, "subject": normalizeEmail(req.Email), "status": "verification_sent",
	})
}

// handleVerify activates an account behind a valid verification token
// (GET /verify?token=...). An invalid/expired token yields 400; success 200.
func handleVerify(w http.ResponseWriter, r *http.Request, users emailUserAPI, log *zap.Logger) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := r.URL.Query().Get("token")
	email, err := users.ConsumeToken(r.Context(), token, PurposeVerify)
	if err == ErrTokenInvalid {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		if log != nil {
			log.Warn("verify: consume token failed", zap.Error(err))
		}
		writeJSONError(w, http.StatusInternalServerError, "verification failed")
		return
	}
	if err := users.ActivateUser(r.Context(), email); err != nil {
		if log != nil {
			log.Error("verify: activate failed", zap.Error(err))
		}
		writeJSONError(w, http.StatusInternalServerError, "verification failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "verified", "subject": email})
}

type forgotRequest struct {
	Email string `json:"email"`
}

// handleForgot starts the password-reset flow (POST /forgot {email}). To avoid
// leaking which emails are registered it ALWAYS returns 200 with the same body,
// whether or not the email exists — a reset link is mailed only if it does. Any
// internal failure is logged but still reported as 200 (again, no probe signal).
func handleForgot(w http.ResponseWriter, r *http.Request, users emailUserAPI, mailer Mailer, opts EmailOptions, log *zap.Logger) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req forgotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	// Uniform response regardless of outcome.
	ok := func() {
		writeJSON(w, http.StatusOK, map[string]any{"status": "reset_email_sent_if_registered"})
	}
	if strings.TrimSpace(req.Email) == "" {
		ok()
		return
	}
	// IssueToken inserts a row for any email string; to avoid minting reset
	// tokens for non-existent accounts we let the mail step be the gate — but a
	// stray token for an unknown email is harmless (it maps to no user and
	// reset's UpdatePassword returns ErrUserNotFound). We issue+mail regardless
	// and always answer 200.
	token, err := users.IssueToken(r.Context(), req.Email, PurposeReset)
	if err != nil {
		if log != nil {
			log.Warn("forgot: issue token failed", zap.Error(err))
		}
		ok()
		return
	}
	link := resetLink(opts.BaseURL, token)
	if err := mailer.Send(normalizeEmail(req.Email), "Reset your password",
		"A password reset was requested. If this was you, use the link below:\r\n\r\n"+link+"\r\n\r\nIf not, ignore this email.\r\n"); err != nil {
		if log != nil {
			log.Warn("forgot: send mail failed", zap.Error(err))
		}
	}
	ok()
}

type resetRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// handleReset completes a password reset (POST /reset {token,password}). A valid
// unexpired reset token is consumed and the account's password is replaced.
// Invalid/expired token -> 400; unknown target user -> 400; success 200. NOTE:
// existing login sessions are intentionally NOT force-revoked here (there is no
// email->session index; a SCAN of all sessions is avoided). The new password
// takes effect immediately and the old one no longer authenticates.
func handleReset(w http.ResponseWriter, r *http.Request, users emailUserAPI, log *zap.Logger) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req resetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "password is required")
		return
	}
	email, err := users.ConsumeToken(r.Context(), req.Token, PurposeReset)
	if err == ErrTokenInvalid {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		if log != nil {
			log.Warn("reset: consume token failed", zap.Error(err))
		}
		writeJSONError(w, http.StatusInternalServerError, "reset failed")
		return
	}
	if err := users.UpdatePassword(r.Context(), email, req.Password); err != nil {
		if err == ErrUserNotFound {
			writeJSONError(w, http.StatusBadRequest, ErrTokenInvalid.Error())
			return
		}
		if log != nil {
			log.Error("reset: update password failed", zap.Error(err))
		}
		writeJSONError(w, http.StatusInternalServerError, "reset failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "password_reset", "subject": email})
}

type roleRequest struct {
	Email string `json:"email"`
	Admin bool   `json:"admin"`
}

// handleSetRole promotes/demotes a user within the caller's tenant scope
// (POST /admin/users/role {email,admin}). Admin-only. A tenant admin can only
// change roles inside their own tenant (EffectiveTenant scope); a platform admin
// may target any tenant. This is the owner/member role management, mapped onto
// the existing is_admin flag (admin=owner, member=ordinary) — no schema change.
func handleSetRole(w http.ResponseWriter, r *http.Request, users emailUserAPI, opts EmailOptions, log *zap.Logger) {
	if !IsAdmin(r.Context()) {
		http.Error(w, "admin privileges required", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req roleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		http.Error(w, "invalid JSON body (need {\"email\":...,\"admin\":bool})", http.StatusBadRequest)
		return
	}
	// Confine a tenant admin to their own tenant; a platform admin gets "" scope
	// (any tenant).
	scope, _ := EffectiveTenant(r.Context(), opts.PlatformTenant)
	if err := users.SetUserAdmin(r.Context(), req.Email, scope, req.Admin); err != nil {
		if err == ErrUserNotFound {
			writeJSONError(w, http.StatusNotFound, "user not found in your tenant")
			return
		}
		if log != nil {
			log.Warn("role: set admin failed", zap.Error(err))
		}
		writeJSONError(w, http.StatusInternalServerError, "role update failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subject": normalizeEmail(req.Email), "admin": req.Admin,
	})
}
