// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"encoding/json"
	"net/http"
	"strconv"

	"go.uber.org/zap"

	"aigis/internal/server"
)

// AdminMiddleware serves the API-key management endpoint at /admin/keys and
// passes every other request through. It plugs into the core purely as a
// server.Middleware (path interception), keeping the ee -> core dependency
// one-way.
//
//	GET    /admin/keys ?tenant=&limit=&offset=  list keys (metadata only, paged)
//	POST   /admin/keys  {key,tenant,subject}    create/enable a key
//	DELETE /admin/keys  {key}                   revoke (soft-disable) a key
//	GET    /admin/keys/audit ?key=&action=&limit=&offset=  key-change audit trail
func AdminMiddleware(p *PostgresAPIKeyProvider, log *zap.Logger) server.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/admin/keys/audit":
				if !IsAdmin(r.Context()) {
					http.Error(w, "admin privileges required", http.StatusForbidden)
					return
				}
				if r.Method != http.MethodGet {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				listAudit(w, r, p, log)
			case "/admin/keys":
				if !IsAdmin(r.Context()) {
					http.Error(w, "admin privileges required", http.StatusForbidden)
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
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

// actorFromRequest identifies the admin principal driving the change, for the
// audit trail. IsAdmin already guarded the route, so a principal is present.
func actorFromRequest(r *http.Request) Actor {
	p, _ := FromContext(r.Context())
	return Actor{Subject: p.Subject, Tenant: p.Tenant}
}

type keyRequest struct {
	Key     string `json:"key"`
	Tenant  string `json:"tenant"`
	Subject string `json:"subject"`
	Admin   bool   `json:"admin"`
}

func listKeys(w http.ResponseWriter, r *http.Request, p *PostgresAPIKeyProvider, log *zap.Logger) {
	q := r.URL.Query()
	keys, err := p.ListKeys(r.Context(), KeyQuery{
		Tenant: q.Get("tenant"),
		Limit:  queryInt(q.Get("limit")),
		Offset: queryInt(q.Get("offset")),
	})
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
	if err := p.CreateKey(r.Context(), req.Key, req.Tenant, req.Subject, req.Admin, actorFromRequest(r)); err != nil {
		writeErr(w, log, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"tenant": req.Tenant, "subject": req.Subject, "admin": req.Admin})
}

func revokeKey(w http.ResponseWriter, r *http.Request, p *PostgresAPIKeyProvider, log *zap.Logger) {
	var req keyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" {
		http.Error(w, "invalid JSON body (need {\"key\":...})", http.StatusBadRequest)
		return
	}
	if err := p.RevokeKey(r.Context(), req.Key, actorFromRequest(r)); err != nil {
		writeErr(w, log, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// queryInt parses a query-string integer, returning 0 for empty/invalid input
// (the ListKeys/ListAudit defaults then apply).
func queryInt(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// listAudit returns key-change audit rows. Optional filters: ?key= (plaintext,
// hashed before matching so raw keys never appear in logs/URLs on the DB side),
// ?action=create|revoke, ?limit=, ?offset=.
func listAudit(w http.ResponseWriter, r *http.Request, p *PostgresAPIKeyProvider, log *zap.Logger) {
	q := r.URL.Query()
	f := AuditFilter{
		Action: q.Get("action"),
		Limit:  queryInt(q.Get("limit")),
		Offset: queryInt(q.Get("offset")),
	}
	if raw := q.Get("key"); raw != "" {
		f.KeyHash = AuditKeyHash(raw)
	}
	rows, err := p.ListAudit(r.Context(), f)
	if err != nil {
		writeErr(w, log, http.StatusInternalServerError, err)
		return
	}
	if rows == nil {
		rows = []AuditRow{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"audit": rows})
}

func writeErr(w http.ResponseWriter, log *zap.Logger, code int, err error) {
	if log != nil {
		log.Warn("admin keys: request failed", zap.Error(err))
	}
	http.Error(w, err.Error(), code)
}
