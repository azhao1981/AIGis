// Package adminui serves a single embedded admin dashboard page and a
// capability-discovery endpoint. The open-source core ships the "status" and
// "routes" panels (health + concurrency metrics + read-only route config). The
// Enterprise Edition lights up additional panels (keys/usage/audit) by overriding
// the capabilities response via a server.Middleware — the same page adapts to
// both builds with no fork.
package adminui

import (
	_ "embed"
	"encoding/json"
	"net/http"
)

//go:embed index.html
var indexHTML []byte

// Capabilities lists which UI panels the running build exposes. The core
// defaults to status-only; the EE middleware replaces this response to add
// its data panels.
type Capabilities struct {
	Panels []string `json:"panels"`
}

// DefaultCapabilities is what the open-source core advertises: a read-only
// gateway status view (health + concurrency) and a read-only routes view
// (config + breaker state), both core features backed by /health, /metrics, and
// /admin/routes-info.
func DefaultCapabilities() Capabilities {
	return Capabilities{Panels: []string{"status", "routes"}}
}

// RegisterRoutes wires the dashboard page and capability endpoint onto the
// core mux. Called from the server's setupRoutes; the EE build layers its
// capability override on top via middleware.
func RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ui", handleIndex)
	mux.HandleFunc("/ui/capabilities", handleCapabilities)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ui" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func handleCapabilities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(DefaultCapabilities())
}
