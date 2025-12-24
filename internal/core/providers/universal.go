package providers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/template"
	"time"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"

	"aigis/internal/core"
	"aigis/internal/core/engine"
	"aigis/internal/core/security"
	"aigis/internal/pkg/logger"
)

// UniversalProvider implements the core.Provider interface with configurable routing
type UniversalProvider struct {
	route   *engine.Route
	client  *http.Client
	scanner *security.Scanner
	log     *logger.Logger
}

// NewUniversalProvider creates a new universal provider for the given route
func NewUniversalProvider(route *engine.Route, log *logger.Logger) *UniversalProvider {
	if log == nil {
		// Create a default logger if none provided
		zapLogger, _ := logger.New("info")
		log = logger.NewLogger(zapLogger)
	}
	return &UniversalProvider{
		route:   route,
		scanner: security.NewScanner(),
		log:     log,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// ID returns the route ID as the provider identifier
func (p *UniversalProvider) ID() string {
	return p.route.ID
}

// Send sends a request through the transformation pipeline to the upstream with header handling
func (p *UniversalProvider) Send(ctx *core.AIGisContext, body []byte, originalHeaders http.Header) ([]byte, error) {
	// Step 1: Apply request transforms (with bidirectional tokenization)
	transformedBody, err := p.applyRequestTransforms(ctx, body)
	if err != nil {
		return nil, fmt.Errorf("transform error: %w", err)
	}

	// Step 2: Prepare and send request with headers
	respBody, err := p.sendToUpstream(ctx.Context, transformedBody, originalHeaders)
	if err != nil {
		return nil, err
	}

	// Step 3: Apply response transforms - unmask placeholders in response content
	finalResp, err := p.applyResponseTransforms(ctx, respBody)
	if err != nil {
		return nil, fmt.Errorf("response transform error: %w", err)
	}

	return finalResp, nil
}

// Stream sends a streaming request (not implemented yet)
func (p *UniversalProvider) Stream(ctx context.Context, body []byte, originalHeaders http.Header) (<-chan []byte, error) {
	return nil, fmt.Errorf("streaming not implemented")
}

// applyRequestTransforms applies all configured transformations to the request body
func (p *UniversalProvider) applyRequestTransforms(ctx *core.AIGisContext, body []byte) ([]byte, error) {
	result := body

	for _, step := range p.route.Transforms {
		var err error
		switch step.Type {
		case engine.TransformTypePII:
			result, err = p.applyPIITransform(ctx, result, step.Config)
		case engine.TransformTypePIIClaude:
			result, err = p.applyClaudePIITransform(ctx, result, step.Config)
		case engine.TransformTypeFieldMap:
			result, err = p.applyFieldMapTransform(result, step.Config)
		case engine.TransformTypeTemplate:
			result, err = p.applyTemplateTransform(result, step.Config)
		default:
			// Unknown transform type, skip
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("transform %s failed: %w", step.Type, err)
		}
	}

	return result, nil
}

// buildUpstreamHeaders constructs headers for upstream request based on HeaderPolicy
func (p *UniversalProvider) buildUpstreamHeaders(originalHeaders http.Header, authHeader http.Header) http.Header {
	upstreamHeaders := make(http.Header)

	// 1. Allow: Copy headers from Allow list
	for _, headerName := range p.route.HeaderPolicy.Allow {
		if value := originalHeaders.Get(headerName); value != "" {
			upstreamHeaders.Set(headerName, value)
		}
	}

	// 2. Set: Force set headers from config
	for key, value := range p.route.HeaderPolicy.Set {
		// Check for env:VAR syntax
		if len(value) >= 4 && value[:4] == "env:" {
			envVar := value[4:]
			envValue := os.Getenv(envVar)
			if envValue != "" {
				upstreamHeaders.Set(key, envValue)
			}
		} else {
			// Literal value
			upstreamHeaders.Set(key, value)
		}
	}

	// 3. Remove: Remove headers from Remove list
	for _, headerName := range p.route.HeaderPolicy.Remove {
		upstreamHeaders.Del(headerName)
	}

	// 4. Auth: Add authentication headers (these override both Allow and Remove)
	for key, values := range authHeader {
		for _, value := range values {
			upstreamHeaders.Add(key, value)
		}
	}

	// Always ensure Content-Type is set
	if upstreamHeaders.Get("Content-Type") == "" {
		upstreamHeaders.Set("Content-Type", "application/json")
	}

	return upstreamHeaders
}

// buildAuthHeaders constructs authentication headers based on the route's AuthStrategy
func (p *UniversalProvider) buildAuthHeaders() http.Header {
	headers := make(http.Header)
	upstream := p.route.Upstream

	token := os.Getenv(upstream.TokenEnv)
	if token == "" {
		return headers
	}

	switch upstream.AuthStrategy {
	case engine.AuthStrategyBearer:
		headers.Set("Authorization", "Bearer "+token)
	case engine.AuthStrategyHeader:
		headerName := upstream.HeaderName
		if headerName == "" {
			headerName = "Authorization"
		}
		headers.Set(headerName, token)
	// AuthStrategyQuery is handled in buildUpstreamURL or query params, not headers
	// We handle default (bearer) as well
	default:
		headers.Set("Authorization", "Bearer "+token)
	}

	return headers
}

// applyPIITransform redacts sensitive information from the request body using bidirectional tokenization
func (p *UniversalProvider) applyPIITransform(ctx *core.AIGisContext, body []byte, config map[string]string) ([]byte, error) {
	// Custom rules can be added from config later if needed
	// For now, we use the scanner's built-in rules

	root, err := sonic.Get(body)
	if err != nil {
		return body, nil // Return original if parse fails
	}

	messagesNode := root.Get("messages")
	if err := messagesNode.Check(); err != nil {
		return body, nil
	}

	if messagesNode.Type() != ast.V_ARRAY {
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

		if contentNode.Type() != ast.V_STRING {
			i++
			continue
		}

		contentStr, err := contentNode.String()
		if err != nil {
			i++
			continue
		}

		// Use Mask() for bidirectional tokenization instead of Sanitize()
		newContent := p.scanner.Mask(ctx, contentStr, nil)

		if newContent != contentStr {
			msgNode.Set("content", ast.NewString(newContent))
		}

		i++
	}

	return root.MarshalJSON()
}

// applyClaudePIITransform redacts PII from Claude/Anthropic format request body using bidirectional tokenization
// Claude format:
//
//	{
//	  "system": "...",  // optional top-level string
//	  "messages": [
//	    {
//	      "role": "user",
//	      "content": "..."  // can be string OR array of blocks
//	    },
//	    {
//	      "role": "assistant",
//	      "content": [
//	        {"type": "text", "text": "..."},
//	        {"type": "image", ...}
//	      ]
//	    }
//	  ]
//	}
func (p *UniversalProvider) applyClaudePIITransform(ctx *core.AIGisContext, body []byte, config map[string]string) ([]byte, error) {
	// Helper function to redact using scanner with Mask()
	redact := func(s string) string {
		return p.scanner.Mask(ctx, s, nil)
	}

	// Parse the body as Sonic AST
	root, err := sonic.Get(body)
	if err != nil {
		return body, nil // Return original if parse fails
	}

	// 1. Handle top-level "system" field (if it exists and is a string)
	systemNode := root.Get("system")
	if err := systemNode.Check(); err == nil && systemNode.Type() == ast.V_STRING {
		if systemStr, err := systemNode.String(); err == nil {
			redactedSystem := redact(systemStr)
			if redactedSystem != systemStr {
				root.Set("system", ast.NewString(redactedSystem))
			}
		}
	}

	// 2. Handle "messages" array
	messagesNode := root.Get("messages")
	if err := messagesNode.Check(); err != nil {
		return root.MarshalJSON() // No messages, return modified root
	}

	if messagesNode.Type() != ast.V_ARRAY {
		return root.MarshalJSON()
	}

	// Iterate through messages
	msgIdx := 0
	for {
		msgNode := messagesNode.Index(msgIdx)
		if err := msgNode.Check(); err != nil {
			break
		}

		// Get the "content" field of this message
		contentNode := msgNode.Get("content")
		if err := contentNode.Check(); err != nil {
			msgIdx++
			continue
		}

		// Content can be either:
		// - A string (simple case, like OpenAI)
		// - An array of blocks (Claude blocks)

		if contentNode.Type() == ast.V_STRING {
			// Simple string content
			if contentStr, err := contentNode.String(); err == nil {
				redactedContent := redact(contentStr)
				if redactedContent != contentStr {
					msgNode.Set("content", ast.NewString(redactedContent))
				}
			}
		} else if contentNode.Type() == ast.V_ARRAY {
			// Array of blocks (Claude format)
			blockIdx := 0
			for {
				blockNode := contentNode.Index(blockIdx)
				if err := blockNode.Check(); err != nil {
					break
				}

				// Check if this is a text block
				typeNode := blockNode.Get("type")
				textNode := blockNode.Get("text")

				typeNodeErr := typeNode.Check()
				textNodeErr := textNode.Check()
				if typeNodeErr == nil && textNodeErr == nil {
					typeStr, typeErr := typeNode.String()
					textStr, textErr := textNode.String()

					if typeErr == nil && textErr == nil && typeStr == "text" {
						// Redact the "text" field using Mask()
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

	result, err := root.MarshalJSON()
	if err != nil {
		return nil, err
	}

	// Debug logging after redaction
	p.log.Debug("Claude PII transform applied",
		// zap.String("original", string(body)),
		zap.String("redacted", string(result)),
	)

	return result, nil
}

// applyFieldMapTransform maps fields from source to target using gjson/sjson
func (p *UniversalProvider) applyFieldMapTransform(body []byte, config map[string]string) ([]byte, error) {
	result := body

	// Config format: "target_path": "source_path"
	// e.g., "inputs.query": "messages.0.content"
	for targetPath, sourcePath := range config {
		// Get value from source path
		value := gjson.GetBytes(body, sourcePath)
		if !value.Exists() {
			continue
		}

		// Set value at target path
		var err error
		if value.Type == gjson.String {
			result, err = sjson.SetBytes(result, targetPath, value.String())
		} else if value.Type == gjson.Number {
			result, err = sjson.SetBytes(result, targetPath, value.Float())
		} else if value.Type == gjson.True || value.Type == gjson.False {
			result, err = sjson.SetBytes(result, targetPath, value.Bool())
		} else {
			// For objects/arrays, set as raw JSON
			result, err = sjson.SetRawBytes(result, targetPath, []byte(value.Raw))
		}

		if err != nil {
			return nil, fmt.Errorf("failed to set field %s: %w", targetPath, err)
		}
	}

	return result, nil
}

// applyTemplateTransform transforms the body using Go text/template
func (p *UniversalProvider) applyTemplateTransform(body []byte, config map[string]string) ([]byte, error) {
	tmplStr := config["template"]
	if tmplStr == "" {
		return body, nil
	}

	// Parse input JSON to map for template data
	var data map[string]interface{}
	if err := sonic.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("failed to parse body for template: %w", err)
	}

	// Parse and execute template
	tmpl, err := template.New("transform").Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("invalid template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("template execution failed: %w", err)
	}

	// Validate output is valid JSON
	result := buf.Bytes()
	if !sonic.Valid(result) {
		return nil, fmt.Errorf("template output is not valid JSON")
	}

	return result, nil
}

// applyResponseTransforms unmask placeholders in the response body
// This restores the original secrets from the vault, only in content fields
func (p *UniversalProvider) applyResponseTransforms(ctx *core.AIGisContext, body []byte) ([]byte, error) {
	// Parse the response body
	root, err := sonic.Get(body)
	if err != nil {
		return body, nil // Return original if parse fails
	}

	// Handle different response formats

	// 1. OpenAI format: choices[].message.content
	choicesNode := root.Get("choices")
	if err := choicesNode.Check(); err == nil && choicesNode.Type() == ast.V_ARRAY {
		i := 0
		for {
			choiceNode := choicesNode.Index(i)
			if err := choiceNode.Check(); err != nil {
				break
			}

			messageNode := choiceNode.Get("message")
			if err := messageNode.Check(); err != nil {
				i++
				continue
			}

			contentNode := messageNode.Get("content")
			if err := contentNode.Check(); err != nil {
				i++
				continue
			}

			if contentNode.Type() == ast.V_STRING {
				if contentStr, err := contentNode.String(); err == nil {
					// Unmask placeholders in content
					unmaskedContent := p.scanner.Unmask(ctx, contentStr)
					if unmaskedContent != contentStr {
						messageNode.Set("content", ast.NewString(unmaskedContent))
					}
				}
			}

			i++
		}
	}

	// 2. Claude format: content[].text (array of blocks)
	contentNode := root.Get("content")
	if err := contentNode.Check(); err == nil && contentNode.Type() == ast.V_ARRAY {
		i := 0
		for {
			blockNode := contentNode.Index(i)
			if err := blockNode.Check(); err != nil {
				break
			}

			// Check if this is a text block
			typeNode := blockNode.Get("type")
			textNode := blockNode.Get("text")

			typeNodeErr := typeNode.Check()
			textNodeErr := textNode.Check()
			if typeNodeErr == nil && textNodeErr == nil {
				typeStr, typeErr := typeNode.String()
				textStr, textErr := textNode.String()

				if typeErr == nil && textErr == nil && typeStr == "text" {
					// Unmask placeholders in text
					unmaskedText := p.scanner.Unmask(ctx, textStr)
					if unmaskedText != textStr {
						blockNode.Set("text", ast.NewString(unmaskedText))
					}
				}
			}

			i++
		}
	}

	return root.MarshalJSON()
}

// sendToUpstream sends the transformed request to the upstream service with header handling
func (p *UniversalProvider) sendToUpstream(ctx context.Context, body []byte, originalHeaders http.Header) ([]byte, error) {
	upstream := p.route.Upstream

	// Build base URL (support env:VAR syntax)
	baseURL := upstream.BaseURL
	if len(baseURL) >= 4 && baseURL[:4] == "env:" {
		envVar := baseURL[4:]
		baseURL = os.Getenv(envVar)
	}

	// Build URL
	path := upstream.Path
	if path == "" {
		path = "/chat/completions" // Default for OpenAI compatibility
	}
	url := baseURL + path

	// Handle query params for AuthStrategyQuery
	if upstream.AuthStrategy == engine.AuthStrategyQuery {
		token := os.Getenv(upstream.TokenEnv)
		if token != "" {
			// Parse URL and add query param
			if reqURL, err := http.NewRequest(http.MethodPost, url, nil); err == nil {
				q := reqURL.URL.Query()
				q.Set("api_key", token) // Common query param name
				reqURL.URL.RawQuery = q.Encode()
				url = reqURL.URL.String()
			}
		}
	}

	// Create request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Build auth headers
	authHeaders := p.buildAuthHeaders()

	// Build all upstream headers using HeaderPolicy
	upstreamHeaders := p.buildUpstreamHeaders(originalHeaders, authHeaders)

	// Apply headers to request
	for key, values := range upstreamHeaders {
		for _, value := range values {
			httpReq.Header.Add(key, value)
		}
	}

	// Execute request
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Handle HTTP errors
	if resp.StatusCode != http.StatusOK {
		return nil, p.handleHTTPError(resp.StatusCode, respBody)
	}

	return respBody, nil
}

// handleHTTPError handles HTTP error responses
func (p *UniversalProvider) handleHTTPError(statusCode int, body []byte) error {
	root, err := sonic.Get(body)
	var errMsg string
	if err == nil {
		// Try OpenAI format first
		errMsg, _ = root.Get("error").Get("message").String()
		if errMsg == "" {
			// Try simple message format
			errMsg, _ = root.Get("message").String()
		}
	}
	if errMsg == "" {
		return fmt.Errorf("HTTP %d: %s", statusCode, string(body))
	}

	switch statusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("unauthorized: %s", errMsg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("rate limit exceeded: %s", errMsg)
	case http.StatusBadRequest:
		return fmt.Errorf("bad request: %s", errMsg)
	default:
		return fmt.Errorf("HTTP %d: %s", statusCode, errMsg)
	}
}
