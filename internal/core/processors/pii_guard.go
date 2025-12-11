package processors

import (
	"fmt"
	"regexp"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"

	"aigis/internal/core"
)

// PIIGuard processor for redacting personally identifiable information
type PIIGuard struct {
	emailRegex *regexp.Regexp
	phoneRegex *regexp.Regexp
}

// NewPIIGuard creates a new PII guard processor
func NewPIIGuard() *PIIGuard {
	// Simple email pattern for MVP
	emailPattern := `[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`
	// Simple mobile phone pattern (international formats) for MVP
	phonePattern := `(\+?1[-.\s]?)?\(?([0-9]{3})\)?[-.\s]?([0-9]{3})[-.\s]?([0-9]{4})`

	return &PIIGuard{
		emailRegex: regexp.MustCompile(emailPattern),
		phoneRegex: regexp.MustCompile(phonePattern),
	}
}

// Name returns the processor name
func (p *PIIGuard) Name() string {
	return "pii-guard"
}

// Priority returns the execution priority (high priority for security)
func (p *PIIGuard) Priority() int {
	return 100
}

// OnRequest processes the request to redact PII using Sonic AST
func (p *PIIGuard) OnRequest(ctx *core.AIGisContext, body []byte) ([]byte, error) {
	root, err := sonic.Get(body)
	if err != nil {
		return body, nil
	}

	messagesNode := root.Get("messages")
	if err := messagesNode.Check(); err != nil {
		return body, nil
	}

	length, err := messagesNode.Len()
	if err != nil || length == 0 {
		return body, nil
	}

	modified := false

	for i := 0; i < length; i++ {
		msgNode := messagesNode.Index(i)
		contentNode := msgNode.Get("content")

		if err := contentNode.Check(); err != nil {
			continue
		}

		if contentNode.Type() != ast.V_STRING {
			continue
		}

		contentStr, err := contentNode.String()
		if err != nil {
			continue
		}

		newContent := contentStr

		// Redact emails
		if p.emailRegex.MatchString(newContent) {
			newContent = p.emailRegex.ReplaceAllString(newContent, "[EMAIL_REDACTED]")
		}

		// Redact phone numbers
		if p.phoneRegex.MatchString(newContent) {
			newContent = p.phoneRegex.ReplaceAllString(newContent, "[PHONE_REDACTED]")
		}

		// Only update if content changed
		if newContent != contentStr {
			msgNode.Set("content", ast.NewString(newContent))
			modified = true
		}
	}

	if modified {
		fmt.Println("PII Detected and Redacted")
	}

	result, err := root.MarshalJSON()
	if err != nil {
		return body, fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return result, nil
}

// OnResponse handles the raw response body (passthrough for now)
func (p *PIIGuard) OnResponse(ctx *core.AIGisContext, body []byte) ([]byte, error) {
	return body, nil
}
