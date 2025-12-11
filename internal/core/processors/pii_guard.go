package processors

import (
	"fmt"
	"regexp"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

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

// OnRequest processes the request to redact PII using gjson/sjson
func (p *PIIGuard) OnRequest(ctx *core.AIGisContext, body []byte) ([]byte, error) {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body, nil
	}

	modified := false
	result := body

	// Iterate through messages
	for i, msg := range messages.Array() {
		content := msg.Get("content")
		if !content.Exists() || content.Type != gjson.String {
			continue
		}

		contentStr := content.String()
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
			path := fmt.Sprintf("messages.%d.content", i)
			var err error
			result, err = sjson.SetBytes(result, path, newContent)
			if err != nil {
				return body, fmt.Errorf("failed to set content at %s: %w", path, err)
			}
			modified = true
		}
	}

	if modified {
		fmt.Println("🔒 PII Detected and Redacted")
	}

	return result, nil
}

// OnResponse handles the raw response body (passthrough for now)
func (p *PIIGuard) OnResponse(ctx *core.AIGisContext, body []byte) ([]byte, error) {
	return body, nil
}
