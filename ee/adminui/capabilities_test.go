// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package adminui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"aigis/internal/adminui"
)

func TestCapabilitiesMiddlewareExtendsPanels(t *testing.T) {
	pass := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := CapabilitiesMiddleware()(pass)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ui/capabilities", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var c adminui.Capabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []string{"status", "routes", "masking", "keys", "usage", "audit"}
	if len(c.Panels) != len(want) {
		t.Fatalf("panels = %v, want %v", c.Panels, want)
	}
	for i, p := range want {
		if c.Panels[i] != p {
			t.Fatalf("panels[%d] = %q, want %q", i, c.Panels[i], p)
		}
	}
}

func TestCapabilitiesMiddlewarePassThrough(t *testing.T) {
	pass := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := CapabilitiesMiddleware()(pass)

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/ui"},
		{http.MethodGet, "/admin/keys"},
		{http.MethodPost, "/ui/capabilities"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusTeapot {
			t.Fatalf("%s %s: intercepted (code %d), want pass-through", tc.method, tc.path, rec.Code)
		}
	}
}
