package providers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"text/template"
	"time"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"aigis/internal/core/engine"
)

// UniversalProvider implements the core.Provider interface with configurable routing
type UniversalProvider struct {
	route  *engine.Route
	client *http.Client
}

// NewUniversalProvider creates a new universal provider for the given route
func NewUniversalProvider(route *engine.Route) *UniversalProvider {
	return &UniversalProvider{
		route: route,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// ID returns the route ID as the provider identifier
func (p *UniversalProvider) ID() string {
	return p.route.ID
}

// Send sends a request through the transformation pipeline to the upstream
func (p *UniversalProvider) Send(ctx context.Context, body []byte) ([]byte, error) {
	// Step 1: Apply request transforms
	transformedBody, err := p.applyRequestTransforms(body)
	if err != nil {
		return nil, fmt.Errorf("transform error: %w", err)
	}

	// Step 2: Prepare and send request
	respBody, err := p.sendToUpstream(ctx, transformedBody)
	if err != nil {
		return nil, err
	}

	// Step 3: Apply response transforms (optional, passthrough for now)
	return respBody, nil
}

// Stream sends a streaming request (not implemented yet)
func (p *UniversalProvider) Stream(ctx context.Context, body []byte) (<-chan []byte, error) {
	return nil, fmt.Errorf("streaming not implemented")
}

// applyRequestTransforms applies all configured transformations to the request body
func (p *UniversalProvider) applyRequestTransforms(body []byte) ([]byte, error) {
	result := body

	for _, step := range p.route.Transforms {
		var err error
		switch step.Type {
		case engine.TransformTypePII:
			result, err = p.applyPIITransform(result, step.Config)
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

// applyPIITransform redacts PII from the request body
func (p *UniversalProvider) applyPIITransform(body []byte, config map[string]string) ([]byte, error) {
	// Reuse patterns from pii_guard, or use custom patterns from config
	emailPattern := config["email_pattern"]
	if emailPattern == "" {
		emailPattern = `[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`
	}
	phonePattern := config["phone_pattern"]
	if phonePattern == "" {
		phonePattern = `(\+?\d{1,3}[-.\s]?)?(\(?\d{3,4}\)?[-.\s]?)?\d{3,4}[-.\s]?\d{4}`
	}

	emailRegex, err := regexp.Compile(emailPattern)
	if err != nil {
		return nil, fmt.Errorf("invalid email pattern: %w", err)
	}
	phoneRegex, err := regexp.Compile(phonePattern)
	if err != nil {
		return nil, fmt.Errorf("invalid phone pattern: %w", err)
	}

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

		newContent := contentStr
		newContent = emailRegex.ReplaceAllString(newContent, "[EMAIL_REDACTED]")
		newContent = phoneRegex.ReplaceAllString(newContent, "[PHONE_REDACTED]")

		if newContent != contentStr {
			msgNode.Set("content", ast.NewString(newContent))
		}

		i++
	}

	return root.MarshalJSON()
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

// sendToUpstream sends the transformed request to the upstream service
func (p *UniversalProvider) sendToUpstream(ctx context.Context, body []byte) ([]byte, error) {
	upstream := p.route.Upstream

	// Build URL
	path := upstream.Path
	if path == "" {
		path = "/chat/completions" // Default for OpenAI compatibility
	}
	url := upstream.BaseURL + path

	// Create request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set content type
	httpReq.Header.Set("Content-Type", "application/json")

	// Set authentication based on strategy
	token := os.Getenv(upstream.TokenEnv)
	if token != "" {
		switch upstream.AuthStrategy {
		case engine.AuthStrategyBearer:
			httpReq.Header.Set("Authorization", "Bearer "+token)
		case engine.AuthStrategyHeader:
			headerName := upstream.HeaderName
			if headerName == "" {
				headerName = "Authorization"
			}
			httpReq.Header.Set(headerName, token)
		case engine.AuthStrategyQuery:
			q := httpReq.URL.Query()
			q.Set("api_key", token) // Common query param name
			httpReq.URL.RawQuery = q.Encode()
		default:
			// Default to bearer
			httpReq.Header.Set("Authorization", "Bearer "+token)
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
