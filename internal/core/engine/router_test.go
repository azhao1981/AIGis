package engine

import "testing"

func newTestEngine(t *testing.T, routes []Route) *Engine {
	t.Helper()
	e, err := NewEngine(&EngineConfig{Routes: routes})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func TestFindRoute_MatchByModelInOrder(t *testing.T) {
	e := newTestEngine(t, []Route{
		{ID: "gpt", Matcher: map[string]string{"model": "^gpt-.*"}},
		{ID: "claude", Matcher: map[string]string{"model": "^(claude|glm).*"}},
		{ID: "fallback", Matcher: map[string]string{}}, // empty matcher = catch-all
	})

	cases := []struct {
		body string
		want string
	}{
		{`{"model":"gpt-4"}`, "gpt"},
		{`{"model":"gpt-3.5-turbo"}`, "gpt"},
		{`{"model":"claude-3-opus"}`, "claude"},
		{`{"model":"glm-4.6"}`, "claude"},
		{`{"model":"llama-3"}`, "fallback"}, // falls through to catch-all
		{`{"other":"x"}`, "fallback"},       // no model field → only catch-all matches
	}
	for _, c := range cases {
		r, err := e.FindRoute([]byte(c.body))
		if err != nil {
			t.Fatalf("FindRoute(%s): %v", c.body, err)
		}
		got := ""
		if r != nil {
			got = r.ID
		}
		if got != c.want {
			t.Errorf("FindRoute(%s) = %q, want %q", c.body, got, c.want)
		}
	}
}

func TestFindRoute_NoMatchReturnsNil(t *testing.T) {
	e := newTestEngine(t, []Route{
		{ID: "gpt", Matcher: map[string]string{"model": "^gpt-.*"}},
	})
	r, err := e.FindRoute([]byte(`{"model":"claude-3"}`))
	if err != nil {
		t.Fatalf("FindRoute: %v", err)
	}
	if r != nil {
		t.Errorf("expected nil route for non-matching model, got %q", r.ID)
	}
}

func TestFindRoute_BadJSONDoesNotMatch(t *testing.T) {
	// A model-matcher route must never match an unparseable body (returns either
	// an error or no route — both yield a nil route, never a false match).
	e := newTestEngine(t, []Route{
		{ID: "gpt", Matcher: map[string]string{"model": "^gpt-.*"}},
	})
	r, _ := e.FindRoute([]byte(`{not valid json`))
	if r != nil {
		t.Errorf("bad JSON should not match a route, got %q", r.ID)
	}
}

func TestNewEngine_InvalidRegex(t *testing.T) {
	_, err := NewEngine(&EngineConfig{Routes: []Route{
		{ID: "bad", Matcher: map[string]string{"model": "["}}, // unterminated class
	}})
	if err == nil {
		t.Fatal("expected error compiling invalid regex, got nil")
	}
}

// TestReload_SwapsRoutesAndCompiles verifies Reload atomically swaps the route
// set + matchers, that FindRoute sees the new routes immediately, and that a
// bad regex on reload returns an error without mutating the live state.
func TestReload_SwapsRoutesAndCompiles(t *testing.T) {
	e := newTestEngine(t, []Route{
		{ID: "gpt", Matcher: map[string]string{"model": "^gpt-.*"}},
		{ID: "fallback", Matcher: map[string]string{}},
	})

	// Confirm baseline.
	r, _ := e.FindRoute([]byte(`{"model":"gpt-4"}`))
	if r == nil || r.ID != "gpt" {
		t.Fatalf("baseline FindRoute gpt: %+v", r)
	}

	// Reload to a totally different route set.
	if err := e.Reload(&EngineConfig{Routes: []Route{
		{ID: "claude", Matcher: map[string]string{"model": "^claude-.*"}},
		{ID: "fallback", Matcher: map[string]string{}},
	}}); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Old route gone, new route present.
	if r, _ := e.FindRoute([]byte(`{"model":"gpt-4"}`)); r == nil || r.ID != "fallback" {
		t.Errorf("after reload gpt-* should fall through, got %+v", r)
	}
	if r, _ := e.FindRoute([]byte(`{"model":"claude-3"}`)); r == nil || r.ID != "claude" {
		t.Errorf("after reload claude-* should hit new route, got %+v", r)
	}

	// Bad regex on reload: error returned, live config preserved.
	bad := &EngineConfig{Routes: []Route{
		{ID: "broken", Matcher: map[string]string{"model": "("}},
	}}
	if err := e.Reload(bad); err == nil {
		t.Fatal("expected error reloading with invalid regex, got nil")
	}
	// The previous reload's claude route must still be live.
	if r, _ := e.FindRoute([]byte(`{"model":"claude-3"}`)); r == nil || r.ID != "claude" {
		t.Errorf("live config must be unchanged after failed reload, got %+v", r)
	}
}
