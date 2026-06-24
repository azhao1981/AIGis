package transform

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"aigis/internal/core"
	"aigis/internal/core/security"
)

var placeholderRe = regexp.MustCompile(`__AIGIS_SEC_[0-9a-f]{12}__`)

func newCtx() *core.AIGisContext {
	return core.NewGatewayContext(context.Background(), nil)
}

func TestPIITransformOpenAI(t *testing.T) {
	scanner := security.NewScanner()
	tr := &PIITransform{name: TypePII, format: formatOpenAI, scanner: scanner}
	ctx := newCtx()

	body := `{"messages":[{"role":"user","content":"reach me at alice@example.com"}]}`
	out, err := tr.Apply(ctx, []byte(body), nil)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got := string(out)
	if strings.Contains(got, "alice@example.com") {
		t.Errorf("email not redacted: %s", got)
	}
	if !placeholderRe.MatchString(got) {
		t.Errorf("expected placeholder in output: %s", got)
	}
	if len(ctx.VaultGetAll()) != 1 {
		t.Errorf("expected 1 vault entry, got %d", len(ctx.VaultGetAll()))
	}
}

func TestPIITransformClaude(t *testing.T) {
	scanner := security.NewScanner()
	tr := &PIITransform{name: TypePIIClaude, format: formatClaude, scanner: scanner}
	ctx := newCtx()

	// system string + content as array of text blocks
	body := `{"system":"contact admin@corp.com","messages":[{"role":"user","content":[{"type":"text","text":"my phone is 13800138000"}]}]}`
	out, err := tr.Apply(ctx, []byte(body), nil)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	got := string(out)
	if strings.Contains(got, "admin@corp.com") {
		t.Errorf("system email not redacted: %s", got)
	}
	if strings.Contains(got, "13800138000") {
		t.Errorf("block phone not redacted: %s", got)
	}
	if len(ctx.VaultGetAll()) != 2 {
		t.Errorf("expected 2 vault entries (email+phone), got %d", len(ctx.VaultGetAll()))
	}
}

func TestFieldMapTransform(t *testing.T) {
	tr := &FieldMapTransform{}
	body := `{"messages":[{"role":"user","content":"hello"}],"max_tokens":42}`
	config := map[string]string{
		"query":      "messages.0.content",
		"max_tokens": "max_tokens",
	}

	out, err := tr.Apply(newCtx(), []byte(body), config)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if v := gjson.GetBytes(out, "query").String(); v != "hello" {
		t.Errorf("expected query=hello, got %q", v)
	}
	if v := gjson.GetBytes(out, "max_tokens").Int(); v != 42 {
		t.Errorf("expected max_tokens=42, got %d", v)
	}
}

func TestTemplateTransform(t *testing.T) {
	tr := &TemplateTransform{}
	body := `{"user":"bob","model":"x"}`
	config := map[string]string{
		"template": `{"inputs":{},"user":"{{.user}}"}`,
	}

	out, err := tr.Apply(newCtx(), []byte(body), config)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	if v := gjson.GetBytes(out, "user").String(); v != "bob" {
		t.Errorf("expected user=bob, got %q", v)
	}
	if !gjson.GetBytes(out, "inputs").Exists() {
		t.Errorf("expected inputs field in output: %s", out)
	}
}

func TestTemplateTransformInvalidJSONOutput(t *testing.T) {
	tr := &TemplateTransform{}
	config := map[string]string{"template": `not json {{.user}}`}
	if _, err := tr.Apply(newCtx(), []byte(`{"user":"x"}`), config); err == nil {
		t.Error("expected error for non-JSON template output")
	}
}

// TestMaskUnmaskRoundTrip verifies PII masking then response unmasking restores
// the original secret through the shared vault.
func TestMaskUnmaskRoundTrip(t *testing.T) {
	scanner := security.NewScanner()
	ctx := newCtx()

	// 1. Mask a request containing a secret
	pii := &PIITransform{name: TypePII, format: formatOpenAI, scanner: scanner}
	reqBody := `{"messages":[{"role":"user","content":"key is sk-abcdefghijklmnopqrstuvwx"}]}`
	masked, err := pii.Apply(ctx, []byte(reqBody), nil)
	if err != nil {
		t.Fatalf("mask failed: %v", err)
	}
	placeholder := placeholderRe.FindString(string(masked))
	if placeholder == "" {
		t.Fatal("no placeholder produced by mask")
	}

	// 2. Simulate an OpenAI response echoing the placeholder, then unmask
	unmask := &UnmaskTransform{scanner: scanner}
	respBody := `{"choices":[{"message":{"role":"assistant","content":"your ` + placeholder + ` is set"}}]}`
	out, err := unmask.Apply(ctx, []byte(respBody), nil)
	if err != nil {
		t.Fatalf("unmask failed: %v", err)
	}

	got := gjson.GetBytes(out, "choices.0.message.content").String()
	if !strings.Contains(got, "sk-abcdefghijklmnopqrstuvwx") {
		t.Errorf("secret not restored: %q", got)
	}
	if placeholderRe.MatchString(got) {
		t.Errorf("placeholder leaked after unmask: %q", got)
	}
}

func TestUnmaskClaudeFormat(t *testing.T) {
	scanner := security.NewScanner()
	ctx := newCtx()
	ctx.VaultStore("__AIGIS_SEC_aabbccddeeff__", "secret@x.com")

	body := `{"content":[{"type":"text","text":"mail: __AIGIS_SEC_aabbccddeeff__"}]}`
	out, err := (&UnmaskTransform{scanner: scanner}).Apply(ctx, []byte(body), nil)
	if err != nil {
		t.Fatalf("unmask failed: %v", err)
	}

	got := gjson.GetBytes(out, "content.0.text").String()
	if !strings.Contains(got, "secret@x.com") {
		t.Errorf("claude text not unmasked: %q", got)
	}
}

func TestRegistryGet(t *testing.T) {
	r := NewDefaultRegistry(security.NewScanner())

	for _, name := range []string{TypePII, TypePIIClaude, TypeFieldMap, TypeTemplate, TypeUnmask} {
		if _, ok := r.Get(name); !ok {
			t.Errorf("expected %q registered", name)
		}
	}
	if _, ok := r.Get("nonexistent"); ok {
		t.Error("expected nonexistent type to be absent")
	}
}
