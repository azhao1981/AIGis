package processors

import (
	"fmt"
	"regexp"

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

// OnRequest processes the request to redact PII
func (p *PIIGuard) OnRequest(ctx *core.AIGisContext, req *core.ModelRequest) error {
	// Iterate through messages
	for i, msg := range req.Messages {
		// Type assert to map[string]interface{}
		msgMap, ok := msg.(map[string]interface{})
		if !ok {
			return fmt.Errorf("message at index %d is not a valid format", i)
		}

		// Get content field
		content, exists := msgMap["content"]
		if !exists {
			continue // Skip if no content field
		}

		// Type assert content to string
		contentStr, ok := content.(string)
		if !ok {
			continue // Skip if content is not a string
		}

		// Track if any PII was detected
		hasPII := false

		// Redact emails
		if p.emailRegex.MatchString(contentStr) {
			contentStr = p.emailRegex.ReplaceAllString(contentStr, "[EMAIL_REDACTED]")
			hasPII = true
		}

		// Redact phone numbers
		if p.phoneRegex.MatchString(contentStr) {
			contentStr = p.phoneRegex.ReplaceAllString(contentStr, "[PHONE_REDACTED]")
			hasPII = true
		}

		// Update the message content if PII was found
		if hasPII {
			msgMap["content"] = contentStr
			req.Messages[i] = msgMap

			// Log the detection (using a simple approach for now)
			fmt.Println("🔒 PII Detected and Redacted")
		}
	}

	return nil
}

// OnResponse is called after the response is received (empty for now)
func (p *PIIGuard) OnResponse(ctx *core.AIGisContext, resp interface{}) error {
	// For now, we don't process responses for PII
	// In the future, we might want to sanitize model responses as well
	return nil
}