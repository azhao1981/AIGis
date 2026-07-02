// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"encoding/json"
	"net/http"

	"go.uber.org/zap"

	"aigis/internal/server"
)

// AdminMiddleware serves the API-key management endpoint at /admin/keys and
// passes every other request through. It plugs into the core purely as a
// server.Middleware (path interception), keeping the ee -> core dependency
// one-way.
//
//	GET    /admin/keys                       list keys (metadata only)
//	POST   /admin/keys  {key,tenant,subject} create/enable a key
//	DELETE /admin/keys  {key}                revoke (soft-disable) a key
func AdminMiddleware(p *PostgresAPIKeyProvider, log *zap.Logger) server.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/admin/keys" {
				next.ServeHTTP(w, r)
				return
			}
			switch r.Method {
			case http.MethodGet:
				listKeys(w, r, p, log)
			case http.MethodPost:
				createKey(w, r, p, log)
			case http.MethodDelete:
				revokeKey(w, r, p, log)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		})
	}
}

type keyRequest struct {
	Key     string `json:"key"`
	Tenant  string `json:"tenant"`
	Subject string `json:"subject"`
}

func listKeys(w http.ResponseWriter, r *http.Request, p *PostgresAPIKeyProvider, log *zap.Logger) {
	keys, err := p.ListKeys(r.Context())
	if err != nil {
		writeErr(w, log, http.StatusInternalServerError, err)
		return
	}
	if keys == nil {
		keys = []KeyInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
}

func createKey(w http.ResponseWriter, r *http.Request, p *PostgresAPIKeyProvider, log *zap.Logger) {
	var req keyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := p.CreateKey(r.Context(), req.Key, req.Tenant, req.Subject); err != nil {
		writeErr(w, log, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"tenant": req.Tenant, "subject": req.Subject})
}

func revokeKey(w http.ResponseWriter, r *http.Request, p *PostgresAPIKeyProvider, log *zap.Logger) {
	var req keyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" {
		http.Error(w, "invalid JSON body (need {\"key\":...})", http.StatusBadRequest)
		return
	}
	if err := p.RevokeKey(r.Context(), req.Key); err != nil {
		writeErr(w, log, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeErr(w http.ResponseWriter, log *zap.Logger, code int, err error) {
	if log != nil {
		log.Warn("admin keys: request failed", zap.Error(err))
	}
	http.Error(w, err.Error(), code)
}
