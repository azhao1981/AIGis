package transform

import (
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"

	"aigis/internal/core"
	"aigis/internal/core/security"
)

// PII format variants. The OpenAI variant redacts string content fields;
// the Claude variant additionally handles the top-level "system" field and
// content arrays of typed blocks.
const (
	formatOpenAI = "openai"
	formatClaude = "claude"
)

// PIITransform redacts sensitive information from a request body using
// bidirectional tokenization (Mask stores placeholder->secret in the vault,
// so a later UnmaskTransform can restore the original in the response).
type PIITransform struct {
	name    string
	format  string
	scanner *security.Scanner
}

func (t *PIITransform) Name() string { return t.name }

func (t *PIITransform) Apply(ctx *core.AIGisContext, body []byte, config map[string]string) ([]byte, error) {
	// "email" config selects email tokenization: "local" preserves the domain,
	// anything else (incl. empty) tokenizes the whole address.
	opts := security.MaskOptions{EmailMode: config["email"]}
	// "rules" config selects which detection rules apply on this route (by rule
	// name, comma-separated, e.g. "Email,Mobile Phone"). Empty = all rules.
	tags := parseTags(config["rules"])
	// "custom_rules" config adds route-scoped detection rules on top of the
	// shared scanner (JSON array of {name, pattern}). They apply only to this
	// route and never mutate the shared scanner.
	extra, err := parseCustomRules(config["custom_rules"])
	if err != nil {
		return nil, err
	}
	if t.format == formatClaude {
		return t.applyClaude(ctx, body, tags, opts, extra)
	}
	return t.applyOpenAI(ctx, body, tags, opts, extra)
}

// parseCustomRules parses the optional "custom_rules" config value (a JSON array
// of {name, pattern} objects) into compiled, route-scoped rules. An empty value
// yields no rules; invalid JSON or a bad regex returns an error so a
// misconfigured route fails loud rather than silently skipping redaction.
func parseCustomRules(s string) ([]security.Rule, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var defs []security.CustomRule
	if err := sonic.Unmarshal([]byte(s), &defs); err != nil {
		return nil, fmt.Errorf("invalid custom_rules JSON: %w", err)
	}
	return security.CompileRules(defs)
}

// parseTags splits a comma-separated rule-name list into trimmed, non-empty
// tags. Returns nil for an empty string, which Mask treats as "all rules".
func parseTags(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	tags := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// applyOpenAI redacts messages[].content string fields plus
// messages[].tool_calls[].function.arguments (OpenAI chat format). Tool-call
// arguments are conversation history echoed back to the upstream on every
// turn, so secrets inside them are an egress channel just like content.
func (t *PIITransform) applyOpenAI(ctx *core.AIGisContext, body []byte, tags []string, opts security.MaskOptions, extra []security.Rule) ([]byte, error) {
	redact := func(s string) string {
		return t.scanner.MaskWithExtraRules(ctx, s, tags, opts, extra)
	}

	root, err := sonic.Get(body)
	if err != nil {
		return body, nil // Return original if parse fails
	}

	messagesNode := root.Get("messages")
	if err := messagesNode.Check(); err != nil {
		return body, nil
	}
	if messagesNode.TypeSafe() != ast.V_ARRAY {
		return body, nil
	}

	i := 0
	for {
		msgNode := messagesNode.Index(i)
		if err := msgNode.Check(); err != nil {
			break
		}

		// content: plain string (user/system/tool-result messages). Assistant
		// tool-call messages often carry content: null — fall through to
		// tool_calls below instead of skipping the message.
		contentNode := msgNode.Get("content")
		if contentNode.Check() == nil && contentNode.TypeSafe() == ast.V_STRING {
			if contentStr, err := contentNode.String(); err == nil {
				if newContent := redact(contentStr); newContent != contentStr {
					msgNode.Set("content", ast.NewString(newContent))
				}
			}
		}

		// tool_calls[].function.arguments: a JSON document as a string. Masked
		// as text — placeholders are quote-free so string values stay intact.
		toolCallsNode := msgNode.Get("tool_calls")
		if toolCallsNode.Check() == nil && toolCallsNode.TypeSafe() == ast.V_ARRAY {
			j := 0
			for {
				tcNode := toolCallsNode.Index(j)
				if err := tcNode.Check(); err != nil {
					break
				}
				fnNode := tcNode.Get("function")
				if fnNode.Check() == nil {
					argsNode := fnNode.Get("arguments")
					if argsNode.Check() == nil && argsNode.TypeSafe() == ast.V_STRING {
						if argsStr, err := argsNode.String(); err == nil {
							if newArgs := redact(argsStr); newArgs != argsStr {
								fnNode.Set("arguments", ast.NewString(newArgs))
							}
						}
					}
				}
				j++
			}
		}
		i++
	}

	return root.MarshalJSON()
}

// maskJSONStrings recursively applies redact to every string leaf of a decoded
// JSON value, preserving structure. Used for tool_use.input objects, where
// masking the raw JSON text could corrupt it (e.g. a phone-number rule matching
// an unquoted JSON number); masking only string leaves keeps the JSON valid.
func maskJSONStrings(redact func(string) string, v interface{}) interface{} {
	switch x := v.(type) {
	case string:
		return redact(x)
	case map[string]interface{}:
		for k, val := range x {
			x[k] = maskJSONStrings(redact, val)
		}
		return x
	case []interface{}:
		for i, val := range x {
			x[i] = maskJSONStrings(redact, val)
		}
		return x
	default:
		return v
	}
}

// applyClaude redacts the top-level "system" string and messages[].content,
// where content can be a string or an array of typed blocks (Claude format).
func (t *PIITransform) applyClaude(ctx *core.AIGisContext, body []byte, tags []string, opts security.MaskOptions, extra []security.Rule) ([]byte, error) {
	redact := func(s string) string {
		return t.scanner.MaskWithExtraRules(ctx, s, tags, opts, extra)
	}

	root, err := sonic.Get(body)
	if err != nil {
		return body, nil // Return original if parse fails
	}

	// 1. Top-level "system" field (if present and a string)
	systemNode := root.Get("system")
	if err := systemNode.Check(); err == nil && systemNode.TypeSafe() == ast.V_STRING {
		if systemStr, err := systemNode.String(); err == nil {
			redactedSystem := redact(systemStr)
			if redactedSystem != systemStr {
				root.Set("system", ast.NewString(redactedSystem))
			}
		}
	}

	// 2. "messages" array
	messagesNode := root.Get("messages")
	if err := messagesNode.Check(); err != nil {
		return root.MarshalJSON()
	}
	if messagesNode.TypeSafe() != ast.V_ARRAY {
		return root.MarshalJSON()
	}

	msgIdx := 0
	for {
		msgNode := messagesNode.Index(msgIdx)
		if err := msgNode.Check(); err != nil {
			break
		}

		contentNode := msgNode.Get("content")
		if err := contentNode.Check(); err != nil {
			msgIdx++
			continue
		}

		if contentNode.TypeSafe() == ast.V_STRING {
			// Simple string content
			if contentStr, err := contentNode.String(); err == nil {
				redactedContent := redact(contentStr)
				if redactedContent != contentStr {
					msgNode.Set("content", ast.NewString(redactedContent))
				}
			}
		} else if contentNode.TypeSafe() == ast.V_ARRAY {
			// Array of typed blocks (Claude format): redact text, tool_use
			// (agent-generated arguments), and tool_result (tool output echoed
			// back) blocks — all three carry request-history text upstream.
			blockIdx := 0
			for {
				blockNode := contentNode.Index(blockIdx)
				if err := blockNode.Check(); err != nil {
					break
				}
				t.redactClaudeBlock(blockNode, redact)
				blockIdx++
			}
		}
		msgIdx++
	}

	return root.MarshalJSON()
}

// redactClaudeBlock redacts one typed content block in place:
//   - "text":        the text field
//   - "tool_use":    every string leaf of the input object (structure kept)
//   - "tool_result": string content, or the text blocks of array content
func (t *PIITransform) redactClaudeBlock(blockNode *ast.Node, redact func(string) string) {
	typeNode := blockNode.Get("type")
	if typeNode.Check() != nil {
		return
	}
	typeStr, err := typeNode.String()
	if err != nil {
		return
	}

	switch typeStr {
	case "text":
		textNode := blockNode.Get("text")
		if textNode.Check() == nil {
			if textStr, err := textNode.String(); err == nil {
				if redacted := redact(textStr); redacted != textStr {
					blockNode.Set("text", ast.NewString(redacted))
				}
			}
		}

	case "tool_use":
		inputNode := blockNode.Get("input")
		if inputNode.Check() != nil {
			return
		}
		raw, err := inputNode.Raw()
		if err != nil {
			return
		}
		var v interface{}
		if sonic.Unmarshal([]byte(raw), &v) != nil {
			return
		}
		masked, err := sonic.Marshal(maskJSONStrings(redact, v))
		if err != nil {
			return
		}
		if _, err := blockNode.Set("input", ast.NewRaw(string(masked))); err != nil {
			return
		}

	case "tool_result":
		contentNode := blockNode.Get("content")
		if contentNode.Check() != nil {
			return
		}
		if contentNode.TypeSafe() == ast.V_STRING {
			if s, err := contentNode.String(); err == nil {
				if redacted := redact(s); redacted != s {
					blockNode.Set("content", ast.NewString(redacted))
				}
			}
		} else if contentNode.TypeSafe() == ast.V_ARRAY {
			k := 0
			for {
				inner := contentNode.Index(k)
				if err := inner.Check(); err != nil {
					break
				}
				t.redactClaudeBlock(inner, redact)
				k++
			}
		}
	}
}
