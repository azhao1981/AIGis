package adminui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIndexServesHTML(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ui", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "<!DOCTYPE html>") {
		t.Fatalf("body does not look like the dashboard page")
	}
}

func TestCapabilitiesDefaultCoreePanels(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ui/capabilities", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var c Capabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// OSS core advertises the read-only status + routes panels (no EE data panels).
	want := []string{"status", "routes"}
	if len(c.Panels) != len(want) {
		t.Fatalf("panels = %v, want %v", c.Panels, want)
	}
	for i, p := range want {
		if c.Panels[i] != p {
			t.Fatalf("panels = %v, want %v", c.Panels, want)
		}
	}
}
