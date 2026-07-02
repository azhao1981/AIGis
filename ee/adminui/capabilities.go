// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

// Package adminui is the Enterprise Edition layer for the embedded admin
// dashboard. The open-source core (internal/adminui) serves the page and a
// status-only capability set; this package plugs in via server.Middleware to
// advertise the additional data panels (keys/usage/audit) the EE build backs
// with /admin/* endpoints. The same page adapts to both builds with no fork,
// keeping the ee -> core dependency one-way.
package adminui

import (
	"encoding/json"
	"net/http"

	"aigis/internal/adminui"
	"aigis/internal/server"
)

// eePanels is what the Enterprise build advertises: the OSS status view plus the
// data panels backed by the /admin/* endpoints (keys/usage/audit).
var eePanels = []string{"status", "keys", "usage", "audit"}

// CapabilitiesMiddleware intercepts GET /ui/capabilities and replaces the core's
// status-only response with the full Enterprise panel set, so the embedded
// dashboard lights up keys/usage/audit. Every other request passes through. It
// plugs into the core purely as a server.Middleware (path interception), keeping
// the ee -> core dependency one-way.
//
// The panels themselves are guarded by the existing auth/admin middlewares on
// the /admin/* routes; this endpoint only tells the page which tabs to show.
func CapabilitiesMiddleware() server.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/ui/capabilities" || r.Method != http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(adminui.Capabilities{Panels: eePanels})
		})
	}
}
