package transform

import (
	"strings"
	"testing"

	"aigis/internal/core/security"
)

// feedDify drives the translator the way the provider does: each upstream chunk
// through Write, then a final Flush, concatenating all emitted bytes.
func feedDify(d *DifyStreamTranslator, chunks []string) string {
	var sb strings.Builder
	for _, c := range chunks {
		out, _ := d.Write([]byte(c))
		sb.WriteString(string(out))
	}
	out, _ := d.Flush()
	sb.WriteString(string(out))
	return sb.String()
}

// difyEvent formats a single Dify SSE event (a "data:" line + blank-line terminator).
func difyEvent(json string) string { return "data: " + json + "\n\n" }

// TestDifyStreamBasicTranslation: message increments become OpenAI delta chunks
// (role only on the first), and message_end yields a stop chunk + [DONE].
func TestDifyStreamBasicTranslation(t *testing.T) {
	d := NewDifyStreamTranslator(security.NewScanner(), newCtx())
	got := feedDify(d, []string{
		difyEvent(`{"event":"message","message_id":"msg-1","created_at":1700000000,"answer":"Hello"}`),
		difyEvent(`{"event":"message","message_id":"msg-1","created_at":1700000000,"answer":" world"}`),
		difyEvent(`{"event":"message_end","message_id":"msg-1","metadata":{"usage":{}}}`),
	})

	if !strings.Contains(got, `"object":"chat.completion.chunk"`) {
		t.Errorf("not translated to OpenAI chunk shape:\n%s", got)
	}
	if !strings.Contains(got, "Hello") || !strings.Contains(got, " world") {
		t.Errorf("answer increments lost:\n%s", got)
	}
	if n := strings.Count(got, `"role":"assistant"`); n != 1 {
		t.Errorf("role should appear exactly once, got %d:\n%s", n, got)
	}
	if !strings.Contains(got, `"id":"chatcmpl-msg-1"`) {
		t.Errorf("message_id not carried into envelope id:\n%s", got)
	}
	if !strings.Contains(got, `"finish_reason":"stop"`) {
		t.Errorf("missing terminal stop chunk:\n%s", got)
	}
	if !strings.Contains(got, "data: [DONE]") {
		t.Errorf("missing [DONE] sentinel:\n%s", got)
	}
}

// TestDifyStreamDropsNonAnswerEvents: control/lifecycle events have no OpenAI
// equivalent and must be dropped, never leaked to the client.
func TestDifyStreamDropsNonAnswerEvents(t *testing.T) {
	d := NewDifyStreamTranslator(security.NewScanner(), newCtx())
	got := feedDify(d, []string{
		difyEvent(`{"event":"ping"}`),
		difyEvent(`{"event":"workflow_started","task_id":"t1"}`),
		difyEvent(`{"event":"node_started","data":{"node_id":"n1"}}`),
		difyEvent(`{"event":"message","message_id":"m","created_at":1,"answer":"hi"}`),
		difyEvent(`{"event":"node_finished","data":{"node_id":"n1"}}`),
		difyEvent(`{"event":"workflow_finished","data":{}}`),
		difyEvent(`{"event":"message_end"}`),
	})

	for _, leaked := range []string{"ping", "workflow", "node_", "task_id"} {
		if strings.Contains(got, leaked) {
			t.Errorf("non-answer event %q leaked to client:\n%s", leaked, got)
		}
	}
	if !strings.Contains(got, "hi") {
		t.Errorf("answer dropped along with control events:\n%s", got)
	}
}

// TestDifyStreamSplitPlaceholder: a tokenization placeholder split across two
// "message" events must be reassembled and restored, with no fragment leaking.
func TestDifyStreamSplitPlaceholder(t *testing.T) {
	scanner := security.NewScanner()
	ctx := newCtx()
	const placeholder = "__AIGIS_SEC_973dfe463ec8__"
	const secret = "alice@example.com"
	ctx.VaultStore(placeholder, secret)

	d := NewDifyStreamTranslator(scanner, ctx)
	got := feedDify(d, []string{
		difyEvent(`{"event":"message","message_id":"m","created_at":1,"answer":"mail: __AIGIS_SEC_973"}`),
		difyEvent(`{"event":"message","message_id":"m","created_at":1,"answer":"dfe463ec8__ ok"}`),
		difyEvent(`{"event":"message_end"}`),
	})

	if !strings.Contains(got, secret) {
		t.Errorf("secret not restored across split message events:\n%s", got)
	}
	if strings.Contains(got, "AIGIS_SEC") {
		t.Errorf("placeholder fragment leaked to client:\n%s", got)
	}
}

// TestDifyStreamTerminatesWithoutMessageEnd: if the upstream stream is cut
// before message_end, Flush must still terminate the client stream cleanly.
func TestDifyStreamTerminatesWithoutMessageEnd(t *testing.T) {
	d := NewDifyStreamTranslator(security.NewScanner(), newCtx())
	got := feedDify(d, []string{
		difyEvent(`{"event":"message","message_id":"m","created_at":1,"answer":"partial"}`),
	})

	if !strings.Contains(got, "partial") {
		t.Errorf("answer lost:\n%s", got)
	}
	if !strings.Contains(got, `"finish_reason":"stop"`) || !strings.Contains(got, "data: [DONE]") {
		t.Errorf("stream not terminated on abrupt end:\n%s", got)
	}
	if n := strings.Count(got, "data: [DONE]"); n != 1 {
		t.Errorf("[DONE] must be emitted exactly once, got %d:\n%s", n, got)
	}
}
