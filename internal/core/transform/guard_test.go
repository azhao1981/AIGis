package transform

import (
	"strings"
	"testing"
)

// TestGuardMaxBytesReject: a body over max_bytes is rejected.
func TestGuardMaxBytesReject(t *testing.T) {
	tr := &GuardTransform{}
	body := `{"messages":[{"role":"user","content":"aaaaaaaaaaaaaaaaaaaa"}]}`

	_, err := tr.Apply(newCtx(), []byte(body), map[string]string{"max_bytes": "10"})
	if err == nil {
		t.Fatal("expected oversized body to be rejected")
	}
	if !strings.Contains(err.Error(), "max_bytes") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestGuardMaxBytesPass: a body within max_bytes passes unchanged.
func TestGuardMaxBytesPass(t *testing.T) {
	tr := &GuardTransform{}
	body := `{"a":1}`

	out, err := tr.Apply(newCtx(), []byte(body), map[string]string{"max_bytes": "1000"})
	if err != nil {
		t.Fatalf("within-limit body should pass: %v", err)
	}
	if string(out) != body {
		t.Errorf("body must be unchanged, got %s", out)
	}
}

// TestGuardMaxTokensReject: a request whose max_tokens exceeds the budget is
// rejected.
func TestGuardMaxTokensReject(t *testing.T) {
	tr := &GuardTransform{}
	body := `{"model":"gpt-4","max_tokens":9000}`

	if _, err := tr.Apply(newCtx(), []byte(body), map[string]string{"max_tokens": "4096"}); err == nil {
		t.Fatal("expected over-budget max_tokens to be rejected")
	}
}

// TestGuardMaxTokensWithinBudget: max_tokens at/under budget passes, and a
// request with no max_tokens field is unaffected.
func TestGuardMaxTokensWithinBudget(t *testing.T) {
	tr := &GuardTransform{}

	for _, body := range []string{
		`{"max_tokens":1000}`, // under budget
		`{"max_tokens":4096}`, // exactly budget
		`{"messages":[]}`,     // no max_tokens field
	} {
		if _, err := tr.Apply(newCtx(), []byte(body), map[string]string{"max_tokens": "4096"}); err != nil {
			t.Errorf("body %s should pass: %v", body, err)
		}
	}
}

// TestGuardInvalidConfig: a non-numeric or negative limit is a config error.
func TestGuardInvalidConfig(t *testing.T) {
	tr := &GuardTransform{}
	body := `{"a":1}`

	cases := []map[string]string{
		{"max_bytes": "abc"},
		{"max_bytes": "-1"},
		{"max_tokens": "xyz"},
		{"max_tokens": "-5"},
	}
	for _, cfg := range cases {
		if _, err := tr.Apply(newCtx(), []byte(body), cfg); err == nil {
			t.Errorf("expected config error for %v", cfg)
		}
	}
}

// TestGuardNoConfig: an empty config is a no-op passthrough.
func TestGuardNoConfig(t *testing.T) {
	tr := &GuardTransform{}
	body := `{"max_tokens":999999}`

	out, err := tr.Apply(newCtx(), []byte(body), nil)
	if err != nil {
		t.Fatalf("no config should be a no-op: %v", err)
	}
	if string(out) != body {
		t.Errorf("body must be unchanged, got %s", out)
	}
}
