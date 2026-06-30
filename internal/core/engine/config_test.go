package engine

import "testing"

func TestResolvedBaseURL(t *testing.T) {
	t.Run("plain url returned unchanged", func(t *testing.T) {
		u := Upstream{BaseURL: "https://api.openai.com/v1"}
		if got := u.ResolvedBaseURL(); got != "https://api.openai.com/v1" {
			t.Errorf("ResolvedBaseURL() = %q, want unchanged plain URL", got)
		}
	})

	t.Run("env: prefix resolved from environment", func(t *testing.T) {
		t.Setenv("AIGIS_TEST_BASE_URL", "https://upstream.example.com")
		u := Upstream{BaseURL: "env:AIGIS_TEST_BASE_URL"}
		if got := u.ResolvedBaseURL(); got != "https://upstream.example.com" {
			t.Errorf("ResolvedBaseURL() = %q, want resolved env value", got)
		}
	})

	t.Run("env: prefix with unset var resolves to empty", func(t *testing.T) {
		u := Upstream{BaseURL: "env:AIGIS_TEST_UNSET_VAR"}
		if got := u.ResolvedBaseURL(); got != "" {
			t.Errorf("ResolvedBaseURL() = %q, want empty for unset env", got)
		}
	})
}
