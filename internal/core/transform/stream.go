package transform

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"aigis/internal/core"
	"aigis/internal/core/security"
)

// StreamTransformer is the stateful contract for transforming a streaming
// (SSE) response chunk-by-chunk. Unlike Transformer it carries state across
// calls, because a single logical value (e.g. a tokenization placeholder) can
// be split across multiple SSE events.
type StreamTransformer interface {
	// Write feeds a slice of upstream bytes and returns bytes to send to the client.
	Write(p []byte) ([]byte, error)
	// Flush is called once at end-of-stream to emit any buffered remainder.
	Flush() ([]byte, error)
}

// sseDelimiter separates SSE events.
var sseDelimiter = []byte("\n\n")

// partialPlaceholderRe matches a trailing fragment that could be any incomplete
// prefix of a placeholder __AIGIS_SEC_<12 hex>__. The hex run allows up to the
// full 12 digits AND an optional trailing `_{0,2}`, so a fragment like
// "__AIGIS_SEC_<12hex>" (hex complete but the closing "__" not yet arrived) is
// still held back instead of leaking. Complete placeholders are restored by
// Unmask before this runs; the leading `_+` also keeps a lone "_" since the
// opening "__" may itself arrive split across two tokens.
var partialPlaceholderRe = regexp.MustCompile(
	`_+(A(I(G(I(S(_(S(E(C(_[0-9a-f]{0,12}_{0,2})?)?)?)?)?)?)?)?)?)?$`)

// carryUnmask holds the cross-event placeholder reassembly state shared by
// stream transformers. A tokenization placeholder can be split across SSE
// events, so feed() holds back a trailing fragment that might still be an
// incomplete placeholder prefix until a later increment completes or rules it
// out. It is COMPOSED into a transformer (each holds one), never embedded as a
// base class.
type carryUnmask struct {
	scanner *security.Scanner
	ctx     *core.AIGisContext
	carry   string
}

// feed accumulates an incoming text increment with the carried-over fragment,
// unmasks any now-complete placeholders, and returns the portion safe to emit
// (holding back a trailing fragment that might still be a partial placeholder).
func (c *carryUnmask) feed(text string) string {
	combined := c.scanner.Unmask(c.ctx, c.carry+text)
	flush, carry := splitSafe(combined)
	c.carry = carry
	return flush
}

// remainder returns and clears any held-back fragment that never completed,
// for emission verbatim at end-of-stream.
func (c *carryUnmask) remainder() string {
	r := c.carry
	c.carry = ""
	return r
}

// StreamUnmasker restores tokenized secrets in a streaming response, handling
// placeholders that are split across multiple SSE text deltas.
//
// It keeps two independent buffers:
//   - eventBuf: raw bytes not yet forming a complete SSE event (split on "\n\n")
//   - cu: a trailing text fragment that may be a partial placeholder, held back
//     across text deltas until it can be completed or ruled out.
type StreamUnmasker struct {
	cu       carryUnmask
	eventBuf []byte
}

// NewStreamUnmasker creates a StreamUnmasker bound to the request context/vault.
func NewStreamUnmasker(scanner *security.Scanner, ctx *core.AIGisContext) *StreamUnmasker {
	return &StreamUnmasker{cu: carryUnmask{scanner: scanner, ctx: ctx}}
}

// Write processes upstream bytes, emitting complete (rewritten) SSE events.
func (u *StreamUnmasker) Write(p []byte) ([]byte, error) {
	u.eventBuf = append(u.eventBuf, p...)

	var out bytes.Buffer
	for {
		idx := bytes.Index(u.eventBuf, sseDelimiter)
		if idx < 0 {
			break // no complete event yet
		}
		event := u.eventBuf[:idx]
		u.eventBuf = u.eventBuf[idx+len(sseDelimiter):]

		if s, skip := u.processEvent(string(event)); !skip {
			out.WriteString(s)
			out.Write(sseDelimiter)
		}
	}
	return out.Bytes(), nil
}

// Flush emits any buffered remainder at end-of-stream.
func (u *StreamUnmasker) Flush() ([]byte, error) {
	var out bytes.Buffer
	if len(u.eventBuf) > 0 {
		if s, skip := u.processEvent(string(u.eventBuf)); !skip {
			out.WriteString(s)
		}
		u.eventBuf = nil
	}
	// Any held-back placeholder fragment never completed: emit it verbatim.
	if rem := u.cu.remainder(); rem != "" {
		out.WriteString(rem)
	}
	return out.Bytes(), nil
}

// processEvent rewrites the text payload of a single SSE event (a block of
// "field: value" lines). Non-text events pass through unchanged. Returns
// skip=true when the event is a text delta whose payload became empty (the
// text was held back as a possible placeholder prefix) — such events are
// dropped to avoid emitting noise; the client reassembles from later deltas.
func (u *StreamUnmasker) processEvent(event string) (out string, skip bool) {
	lines := strings.Split(event, "\n")
	isTextDelta, emptyFlush := false, false
	for i, line := range lines {
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimPrefix(data, " ")
		if !gjson.Valid(data) {
			continue
		}
		if newData, rewritten, empty := u.rewriteTextDelta(data); rewritten {
			isTextDelta, emptyFlush = true, empty
			lines[i] = "data: " + newData
		}
	}
	if isTextDelta && emptyFlush {
		return "", true
	}
	return strings.Join(lines, "\n"), false
}

// rewriteTextDelta detects a text-increment event (Claude or OpenAI), runs its
// text through the cross-chunk unmask pipeline, and writes the result back.
// Returns (newJSON, true, emptyFlush) if it was a text delta — emptyFlush is
// true when the entire increment was held back. Returns (data, false, false)
// for non-text events.
func (u *StreamUnmasker) rewriteTextDelta(data string) (string, bool, bool) {
	// Claude: {"type":"content_block_delta","delta":{"type":"text_delta","text":"..."}}
	if gjson.Get(data, "type").String() == "content_block_delta" {
		if t := gjson.Get(data, "delta.text"); t.Exists() {
			flush := u.cu.feed(t.String())
			out, err := sjson.Set(data, "delta.text", flush)
			if err != nil {
				return data, false, false
			}
			return out, true, flush == ""
		}
	}
	// OpenAI: {"choices":[{"delta":{"content":"..."}}]}
	if c := gjson.Get(data, "choices.0.delta.content"); c.Exists() {
		flush := u.cu.feed(c.String())
		out, err := sjson.Set(data, "choices.0.delta.content", flush)
		if err != nil {
			return data, false, false
		}
		return out, true, flush == ""
	}
	return data, false, false
}

// splitSafe separates s into the part safe to emit now and a trailing fragment
// that could still be the beginning of an incomplete placeholder.
func splitSafe(s string) (flush, carry string) {
	if loc := partialPlaceholderRe.FindStringIndex(s); loc != nil {
		return s[:loc[0]], s[loc[0]:]
	}
	return s, ""
}

// Stream translator names used in route config (stream_translate).
const (
	StreamTranslateUnmask = "unmask" // passthrough + cross-chunk unmask (default)
	StreamTranslateDify   = "dify"   // Dify SSE -> OpenAI chunk SSE
)

// NewStreamTransformer builds the stream response transformer named by a route's
// stream_translate field. Empty or "unmask" yields the default passthrough
// unmasker (existing behavior); "dify" yields the Dify->OpenAI translator.
// ok is false for an unknown name (config validation rejects those at startup).
func NewStreamTransformer(name string, scanner *security.Scanner, ctx *core.AIGisContext) (StreamTransformer, bool) {
	switch name {
	case "", StreamTranslateUnmask:
		return NewStreamUnmasker(scanner, ctx), true
	case StreamTranslateDify:
		return NewDifyStreamTranslator(scanner, ctx), true
	default:
		return nil, false
	}
}

// KnownStreamTranslators returns the valid stream_translate values, injected
// into config validation so a typo fails loud at startup (mirrors KnownTypes).
func KnownStreamTranslators() map[string]bool {
	return map[string]bool{
		"":                    true,
		StreamTranslateUnmask: true,
		StreamTranslateDify:   true,
	}
}
