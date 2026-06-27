package transform

import (
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
	if t.format == formatClaude {
		return t.applyClaude(ctx, body, tags, opts)
	}
	return t.applyOpenAI(ctx, body, tags, opts)
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

// applyOpenAI redacts messages[].content string fields (OpenAI chat format).
func (t *PIITransform) applyOpenAI(ctx *core.AIGisContext, body []byte, tags []string, opts security.MaskOptions) ([]byte, error) {
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

		contentNode := msgNode.Get("content")
		if err := contentNode.Check(); err != nil {
			i++
			continue
		}
		if contentNode.TypeSafe() != ast.V_STRING {
			i++
			continue
		}

		contentStr, err := contentNode.String()
		if err != nil {
			i++
			continue
		}

		newContent := t.scanner.MaskWithOptions(ctx, contentStr, tags, opts)
		if newContent != contentStr {
			msgNode.Set("content", ast.NewString(newContent))
		}
		i++
	}

	return root.MarshalJSON()
}

// applyClaude redacts the top-level "system" string and messages[].content,
// where content can be a string or an array of typed blocks (Claude format).
func (t *PIITransform) applyClaude(ctx *core.AIGisContext, body []byte, tags []string, opts security.MaskOptions) ([]byte, error) {
	redact := func(s string) string {
		return t.scanner.MaskWithOptions(ctx, s, tags, opts)
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
			// Array of typed blocks (Claude format): redact text blocks
			blockIdx := 0
			for {
				blockNode := contentNode.Index(blockIdx)
				if err := blockNode.Check(); err != nil {
					break
				}

				typeNode := blockNode.Get("type")
				textNode := blockNode.Get("text")
				if typeNode.Check() == nil && textNode.Check() == nil {
					typeStr, typeErr := typeNode.String()
					textStr, textErr := textNode.String()
					if typeErr == nil && textErr == nil && typeStr == "text" {
						redactedText := redact(textStr)
						if redactedText != textStr {
							blockNode.Set("text", ast.NewString(redactedText))
						}
					}
				}
				blockIdx++
			}
		}
		msgIdx++
	}

	return root.MarshalJSON()
}
