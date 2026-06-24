package transform

import (
	"strings"
	"testing"

	"aigis/internal/core/security"
)

// feedChunks writes each chunk through the unmasker and concatenates output,
// then appends the final flush — mirroring how the provider drives it.
func feedChunks(u *StreamUnmasker, chunks []string) string {
	var sb strings.Builder
	for _, c := range chunks {
		out, _ := u.Write([]byte(c))
		sb.Write(out)
	}
	out, _ := u.Flush()
	sb.Write(out)
	return sb.String()
}

// TestStreamUnmaskClaudeSplitPlaceholder reproduces the real bug: a placeholder
// split across three content_block_delta events must be reassembled and restored.
func TestStreamUnmaskClaudeSplitPlaceholder(t *testing.T) {
	scanner := security.NewScanner()
	ctx := newCtx()
	const placeholder = "__AIGIS_SEC_973dfe463ec8__"
	const secret = "test@example.com"
	ctx.VaultStore(placeholder, secret)

	u := NewStreamUnmasker(scanner, ctx)

	// The placeholder "__AIGIS_SEC_973dfe463ec8__" is split as the upstream
	// emitted it: "邮箱是 __" / "AIGIS_SEC_973" / "dfe463ec8__".
	chunks := []string{
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"邮箱是 __"}}` + "\n\n",
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"AIGIS_SEC_973"}}` + "\n\n",
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"dfe463ec8__"}}` + "\n\n",
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n",
	}

	got := feedChunks(u, chunks)

	if !strings.Contains(got, secret) {
		t.Errorf("secret not restored across split deltas:\n%s", got)
	}
	if strings.Contains(got, "AIGIS_SEC") {
		t.Errorf("placeholder fragment leaked to client:\n%s", got)
	}
	// Empty text deltas (held-back placeholder fragments) must be dropped.
	if strings.Contains(got, `"text":""`) {
		t.Errorf("empty text delta not skipped:\n%s", got)
	}
	// Non-text event must pass through untouched.
	if !strings.Contains(got, `"type":"message_stop"`) {
		t.Errorf("message_stop event not preserved:\n%s", got)
	}
}

// TestStreamUnmaskOpenAISplitPlaceholder covers the OpenAI chunk format.
func TestStreamUnmaskOpenAISplitPlaceholder(t *testing.T) {
	scanner := security.NewScanner()
	ctx := newCtx()
	const placeholder = "__AIGIS_SEC_aabbccddeeff__"
	const secret = "sk-realsecretkey1234567890"
	ctx.VaultStore(placeholder, secret)

	u := NewStreamUnmasker(scanner, ctx)
	chunks := []string{
		`data: {"choices":[{"delta":{"content":"key: __AIG"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"IS_SEC_aabbccdd"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"eeff__ done"}}]}` + "\n\n",
		`data: [DONE]` + "\n\n",
	}

	got := feedChunks(u, chunks)

	if !strings.Contains(got, secret) {
		t.Errorf("secret not restored:\n%s", got)
	}
	if strings.Contains(got, "AIGIS_SEC") {
		t.Errorf("placeholder fragment leaked:\n%s", got)
	}
	if !strings.Contains(got, "[DONE]") {
		t.Errorf("[DONE] sentinel not preserved:\n%s", got)
	}
}

// TestStreamUnmaskPlainTextUnaffected ensures ordinary text streams through
// without being held back or altered.
func TestStreamUnmaskPlainTextUnaffected(t *testing.T) {
	u := NewStreamUnmasker(security.NewScanner(), newCtx())
	chunks := []string{
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}` + "\n\n",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}` + "\n\n",
	}
	got := feedChunks(u, chunks)
	if !strings.Contains(got, "hello ") || !strings.Contains(got, "world") {
		t.Errorf("plain text altered/dropped:\n%s", got)
	}
}

func TestSplitSafe(t *testing.T) {
	cases := []struct {
		in, flush, carry string
	}{
		{"hello world", "hello world", ""},
		{"邮箱是 __", "邮箱是 ", "__"},
		{"x __AIGIS_SEC_973", "x ", "__AIGIS_SEC_973"},
		// hex complete (12) but closing "__" not yet arrived — must be held back
		{"x __AIGIS_SEC_973dfe463ec8", "x ", "__AIGIS_SEC_973dfe463ec8"},
		{"x __AIGIS_SEC_973dfe463ec8_", "x ", "__AIGIS_SEC_973dfe463ec8_"},
		{"done __AIGIS_SEC_", "done ", "__AIGIS_SEC_"},
		{"trailing _", "trailing ", "_"},
		{"no underscore here", "no underscore here", ""},
	}
	for _, c := range cases {
		f, ca := splitSafe(c.in)
		if f != c.flush || ca != c.carry {
			t.Errorf("splitSafe(%q) = (%q,%q), want (%q,%q)", c.in, f, ca, c.flush, c.carry)
		}
	}
}
