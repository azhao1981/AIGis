// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package billing

import (
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"

	"aigis/ee/auth"
	"aigis/internal/server"
)

// AdminMiddleware returns a server.Middleware that serves the read-only usage
// query endpoint at GET /admin/usage and passes every other request through to
// the gateway. It is the sole seam between the EE billing admin API and the OSS
// core — the core exposes no route-registration hook, so we intercept by path.
//
//	GET /admin/usage?tenant=&from=&to=&granularity=hour|day|month
//	  tenant      optional; empty = all tenants
//	  from,to     RFC3339 timestamps; default = last 30 days
//	  granularity date_trunc unit; default = day
func AdminMiddleware(sink *PostgresSink, log *zap.Logger) server.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/admin/usage" {
				next.ServeHTTP(w, r)
				return
			}
			if !auth.IsAdmin(r.Context()) {
				http.Error(w, "admin privileges required", http.StatusForbidden)
				return
			}
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			handleUsage(w, r, sink, log)
		})
	}
}

func handleUsage(w http.ResponseWriter, r *http.Request, sink *PostgresSink, log *zap.Logger) {
	q := r.URL.Query()
	uq := UsageQuery{
		Tenant:      q.Get("tenant"),
		Granularity: q.Get("granularity"),
	}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "invalid 'from' (want RFC3339)", http.StatusBadRequest)
			return
		}
		uq.From = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "invalid 'to' (want RFC3339)", http.StatusBadRequest)
			return
		}
		uq.To = t
	}

	rows, err := sink.QueryUsage(r.Context(), uq)
	if err != nil {
		if log != nil {
			log.Warn("admin: usage query failed", zap.Error(err))
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if rows == nil {
		rows = []UsageRow{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"usage": rows})
}
