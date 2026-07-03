// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package billing

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
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
//	GET /admin/usage?tenant=&from=&to=&granularity=hour|day|month&format=json|csv
//	  tenant      optional; empty = all tenants
//	  from,to     RFC3339 timestamps; default = last 30 days
//	  granularity date_trunc unit; default = day
//	  format      json (default) or csv (downloadable attachment)
//
// platformTenant names the tenant whose admins see every tenant's usage. Any
// other admin is a tenant admin and only sees their own tenant's usage — the
// ?tenant= filter is ignored for them (batch B multi-tenant isolation).
func AdminMiddleware(sink *PostgresSink, platformTenant string, log *zap.Logger) server.Middleware {
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
			handleUsage(w, r, sink, platformTenant, log)
		})
	}
}

func handleUsage(w http.ResponseWriter, r *http.Request, sink *PostgresSink, platformTenant string, log *zap.Logger) {
	q := r.URL.Query()
	// A tenant admin is confined to their own tenant; a platform admin (scope "")
	// may filter across tenants via ?tenant=.
	tenant := q.Get("tenant")
	if scope, isPlatform := auth.EffectiveTenant(r.Context(), platformTenant); !isPlatform {
		tenant = scope
	}
	uq := UsageQuery{
		Tenant:      tenant,
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

	if q.Get("format") == "csv" {
		writeUsageCSV(w, rows)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"usage": rows})
}

// writeUsageCSV streams the usage rows as a downloadable CSV (one header row +
// one row per bucket), for spreadsheet/finance workflows.
func writeUsageCSV(w http.ResponseWriter, rows []UsageRow) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="usage.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"bucket", "tenant", "requests", "prompt_tokens", "completion_tokens", "total_tokens"})
	for _, row := range rows {
		_ = cw.Write([]string{
			row.Bucket.Format(time.RFC3339),
			row.Tenant,
			strconv.FormatInt(row.Requests, 10),
			strconv.FormatInt(row.PromptTokens, 10),
			strconv.FormatInt(row.CompletionTokens, 10),
			strconv.FormatInt(row.TotalTokens, 10),
		})
	}
	cw.Flush()
}
