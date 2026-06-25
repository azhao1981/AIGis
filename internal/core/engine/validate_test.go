package engine

import (
	"strings"
	"testing"
)

// known mirrors transform.KnownTypes() without importing transform (avoids a
// test-only dependency); engine.Validate only cares about set membership.
var known = map[string]bool{
	"pii": true, "pii_claude": true, "field_map": true, "template": true, "unmask": true,
}

// validRoutes returns a minimal config that passes Validate: one model route
// plus a catch-all. Callers mutate it to exercise a single failure mode.
func validRoutes() []Route {
	return []Route{
		{
			ID:       "gpt",
			Matcher:  map[string]string{"model": "^gpt-.*"},
			Upstream: Upstream{BaseURL: "https://api.openai.com/v1", AuthStrategy: "bearer"},
			Transforms: []TransformStep{
				{Type: "pii"},
			},
		},
		{
			ID:       "fallback",
			Matcher:  map[string]string{},
			Upstream: Upstream{BaseURL: "https://api.openai.com/v1", AuthStrategy: "bearer"},
		},
	}
}

func TestValidate_OK(t *testing.T) {
	c := &EngineConfig{Routes: validRoutes()}
	if err := c.Validate(known); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

func TestValidate_Failures(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(r []Route) []Route
		wantSub string
	}{
		{
			name:    "no routes",
			mutate:  func(_ []Route) []Route { return nil },
			wantSub: "no routes",
		},
		{
			name:    "empty id",
			mutate:  func(r []Route) []Route { r[0].ID = ""; return r },
			wantSub: "empty id",
		},
		{
			name:    "duplicate id",
			mutate:  func(r []Route) []Route { r[1].ID = r[0].ID; return r },
			wantSub: "duplicate route id",
		},
		{
			name:    "invalid regex",
			mutate:  func(r []Route) []Route { r[0].Matcher["model"] = "["; return r },
			wantSub: "invalid regex",
		},
		{
			name:    "empty base_url",
			mutate:  func(r []Route) []Route { r[0].Upstream.BaseURL = ""; return r },
			wantSub: "base_url is empty",
		},
		{
			name:    "unknown auth_strategy",
			mutate:  func(r []Route) []Route { r[0].Upstream.AuthStrategy = "magic"; return r },
			wantSub: "unknown auth_strategy",
		},
		{
			name:    "unknown transform",
			mutate:  func(r []Route) []Route { r[0].Transforms[0].Type = "shrink"; return r },
			wantSub: "unknown transform type",
		},
		{
			name: "no catch-all",
			mutate: func(r []Route) []Route {
				r[1].Matcher = map[string]string{"model": "^x-.*"} // fallback now has a matcher
				return r
			},
			wantSub: "catch-all",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &EngineConfig{Routes: tc.mutate(validRoutes())}
			err := c.Validate(known)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// "none" and "" auth strategies are valid (claude-proxy uses none + header_policy).
func TestValidate_NoneAuthStrategyOK(t *testing.T) {
	r := validRoutes()
	r[0].Upstream.AuthStrategy = "none"
	r[1].Upstream.AuthStrategy = ""
	if err := (&EngineConfig{Routes: r}).Validate(known); err != nil {
		t.Fatalf("none/empty auth_strategy should be valid, got: %v", err)
	}
}
