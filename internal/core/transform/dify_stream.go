package transform

import (
	"bytes"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/tidwall/gjson"

	"aigis/internal/core"
	"aigis/internal/core/security"
)

// DifyStreamTranslator adapts a Dify chat SSE stream into OpenAI
// chat.completion.chunk SSE, restoring tokenized secrets in the answer text.
//
// It is an Adapter (Dify protocol -> OpenAI protocol) exposed behind the
// StreamTransformer (Strategy) contract, selected per-route via stream_translate.
// Dify emits many event kinds; only the answer-bearing ones are translated:
//
//	message / agent_message -> a delta chunk carrying the answer increment
//	message_end             -> a final stop chunk + "data: [DONE]"
//	everything else (ping, workflow_started, node_*, workflow_finished,
//	tts_message, message_replace, error) is dropped — there is no OpenAI
//	streaming equivalent, and dropping keeps the client stream clean.
//
// The cross-event placeholder reassembly (carryUnmask) is the same logic the
// passthrough StreamUnmasker uses, so a secret split across two "message"
// events is restored correctly.
type DifyStreamTranslator struct {
	cu       carryUnmask
	buf      []byte // raw upstream bytes not yet forming a complete SSE event
	id       string // OpenAI envelope id, captured from the first message event
	created  int64  // OpenAI envelope created, captured from the first message event
	roleSent bool   // whether the leading {"role":"assistant"} delta was emitted
	doneSent bool   // whether the terminal stop chunk + [DONE] was emitted
}

// NewDifyStreamTranslator creates a translator bound to the request context/vault.
func NewDifyStreamTranslator(scanner *security.Scanner, ctx *core.AIGisContext) *DifyStreamTranslator {
	return &DifyStreamTranslator{cu: carryUnmask{scanner: scanner, ctx: ctx}}
}

// openAIChunk is the OpenAI chat.completion.chunk envelope emitted per delta.
type openAIChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
}

type openAIChoice struct {
	Index        int         `json:"index"`
	Delta        openAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type openAIDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// Write feeds upstream bytes and returns translated OpenAI SSE for complete events.
func (d *DifyStreamTranslator) Write(p []byte) ([]byte, error) {
	d.buf = append(d.buf, p...)

	var out bytes.Buffer
	for {
		idx := bytes.Index(d.buf, sseDelimiter)
		if idx < 0 {
			break // no complete event yet
		}
		event := d.buf[:idx]
		d.buf = d.buf[idx+len(sseDelimiter):]
		out.WriteString(d.processEvent(string(event)))
	}
	return out.Bytes(), nil
}

// Flush drains the final buffered event, emits any held-back placeholder
// remainder, and guarantees the stream is terminated with a stop chunk + [DONE]
// even if the upstream never sent message_end.
func (d *DifyStreamTranslator) Flush() ([]byte, error) {
	var out bytes.Buffer
	if len(d.buf) > 0 {
		out.WriteString(d.processEvent(string(d.buf)))
		d.buf = nil
	}
	if rem := d.cu.remainder(); rem != "" {
		out.WriteString(d.chunk(d.delta(rem), nil))
	}
	out.WriteString(d.finish())
	return out.Bytes(), nil
}

// processEvent translates one Dify SSE event into zero or more OpenAI SSE chunks.
func (d *DifyStreamTranslator) processEvent(event string) string {
	data := difyEventData(event)
	if data == "" || !gjson.Valid(data) {
		return ""
	}

	switch gjson.Get(data, "event").String() {
	case "message", "agent_message", "message_replace":
		// message_replace is emitted when output moderation rewrites the answer.
		// Streaming can't retract deltas already sent to the client, so we surface
		// the replacement text as an additional delta (the original is unavoidably
		// already on the wire — use blocking mode if true retraction is required).
		if d.id == "" {
			d.id = gjson.Get(data, "message_id").String()
			d.created = gjson.Get(data, "created_at").Int()
		}
		flush := d.cu.feed(gjson.Get(data, "answer").String())
		if flush == "" {
			return "" // increment fully held back as a possible placeholder prefix
		}
		return d.chunk(d.delta(flush), nil)
	case "message_end":
		return d.finish()
	default:
		return "" // ping / workflow_* / node_* / tts_message / error
	}
}

// delta builds the OpenAI delta for a content increment, attaching the leading
// {"role":"assistant"} exactly once (on the first emitted chunk).
func (d *DifyStreamTranslator) delta(content string) openAIDelta {
	dl := openAIDelta{Content: content}
	if !d.roleSent {
		dl.Role = "assistant"
		d.roleSent = true
	}
	return dl
}

// chunk marshals one OpenAI chat.completion.chunk SSE event.
func (d *DifyStreamTranslator) chunk(delta openAIDelta, finish *string) string {
	id := "chatcmpl-dify"
	if d.id != "" {
		id = "chatcmpl-" + d.id
	}
	c := openAIChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: d.created,
		Model:   "dify",
		Choices: []openAIChoice{{Index: 0, Delta: delta, FinishReason: finish}},
	}
	b, err := sonic.Marshal(c)
	if err != nil {
		return ""
	}
	return "data: " + string(b) + "\n\n"
}

// finish emits the terminal stop chunk and "data: [DONE]" exactly once.
func (d *DifyStreamTranslator) finish() string {
	if d.doneSent {
		return ""
	}
	d.doneSent = true
	stop := "stop"
	return d.chunk(openAIDelta{}, &stop) + "data: [DONE]\n\n"
}

// difyEventData returns the JSON payload of an SSE event's "data:" line.
func difyEventData(event string) string {
	for _, line := range strings.Split(event, "\n") {
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			return strings.TrimPrefix(data, " ")
		}
	}
	return ""
}
