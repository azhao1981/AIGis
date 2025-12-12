package providers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/bytedance/sonic"
)

// OpenAIProvider implements the core.Provider interface for OpenAI API
type OpenAIProvider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewOpenAIProvider creates a new OpenAI provider
func NewOpenAIProvider(apiKey, baseURL string) *OpenAIProvider {
	return &OpenAIProvider{
		apiKey:  apiKey,
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// ID returns the unique identifier for this provider
func (p *OpenAIProvider) ID() string {
	return "openai"
}

// Send sends a raw request body to OpenAI and returns the raw response body
func (p *OpenAIProvider) Send(ctx context.Context, body []byte) ([]byte, error) {
	// Create HTTP request
	url := p.baseURL + "/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	// Execute request
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
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

// Stream sends a streaming request to OpenAI (not implemented yet)
func (p *OpenAIProvider) Stream(ctx context.Context, body []byte) (<-chan []byte, error) {
	return nil, fmt.Errorf("streaming not implemented")
}

// handleHTTPError handles HTTP error responses from OpenAI
func (p *OpenAIProvider) handleHTTPError(statusCode int, body []byte) error {
	root, err := sonic.Get(body)
	var errMsg string
	if err == nil {
		errMsg, _ = root.Get("error").Get("message").String()
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
