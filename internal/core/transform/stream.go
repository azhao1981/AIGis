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

// StreamUnmasker restores tokenized secrets in a streaming response, handling
// placeholders that are split across multiple SSE text deltas.
//
// It keeps two independent buffers:
//   - eventBuf: raw bytes not yet forming a complete SSE event (split on "\n\n")
//   - textCarry: a trailing text fragment that may be a partial placeholder,
//     held back across text deltas until it can be completed or ruled out.
type StreamUnmasker struct {
	scanner   *security.Scanner
	ctx       *core.AIGisContext
	eventBuf  []byte
	textCarry string
}

// NewStreamUnmasker creates a StreamUnmasker bound to the request context/vault.
func NewStreamUnmasker(scanner *security.Scanner, ctx *core.AIGisContext) *StreamUnmasker {
	return &StreamUnmasker{scanner: scanner, ctx: ctx}
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
	if u.textCarry != "" {
		out.WriteString(u.textCarry)
		u.textCarry = ""
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
			flush := u.feed(t.String())
			out, err := sjson.Set(data, "delta.text", flush)
			if err != nil {
				return data, false, false
			}
			return out, true, flush == ""
		}
	}
	// OpenAI: {"choices":[{"delta":{"content":"..."}}]}
	if c := gjson.Get(data, "choices.0.delta.content"); c.Exists() {
		flush := u.feed(c.String())
		out, err := sjson.Set(data, "choices.0.delta.content", flush)
		if err != nil {
			return data, false, false
		}
		return out, true, flush == ""
	}
	return data, false, false
}

// feed accumulates an incoming text increment with the carried-over fragment,
// unmasks any now-complete placeholders, and returns the portion safe to emit
// (holding back a trailing fragment that might still be a partial placeholder).
func (u *StreamUnmasker) feed(text string) string {
	combined := u.scanner.Unmask(u.ctx, u.textCarry+text)
	flush, carry := splitSafe(combined)
	u.textCarry = carry
	return flush
}

// splitSafe separates s into the part safe to emit now and a trailing fragment
// that could still be the beginning of an incomplete placeholder.
func splitSafe(s string) (flush, carry string) {
	if loc := partialPlaceholderRe.FindStringIndex(s); loc != nil {
		return s[:loc[0]], s[loc[0]:]
	}
	return s, ""
}
