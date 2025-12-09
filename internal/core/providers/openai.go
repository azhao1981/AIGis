package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"aigis/internal/core"
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

// Send sends a non-streaming request to OpenAI and returns the response
func (p *OpenAIProvider) Send(ctx context.Context, req *core.ModelRequest) (interface{}, error) {
	// Marshal request to JSON
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	url := p.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
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
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Handle HTTP errors
	if resp.StatusCode != http.StatusOK {
		return nil, p.handleHTTPError(resp.StatusCode, body)
	}

	// Unmarshal response
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return result, nil
}

// Stream sends a streaming request to OpenAI (not implemented yet)
func (p *OpenAIProvider) Stream(ctx context.Context, req *core.ModelRequest) (<-chan string, error) {
	return nil, fmt.Errorf("streaming not implemented")
}

// handleHTTPError handles HTTP error responses from OpenAI
func (p *OpenAIProvider) handleHTTPError(statusCode int, body []byte) error {
	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &errResp); err != nil {
		return fmt.Errorf("HTTP %d: %s", statusCode, string(body))
	}

	switch statusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("unauthorized: %s", errResp.Error.Message)
	case http.StatusTooManyRequests:
		return fmt.Errorf("rate limit exceeded: %s", errResp.Error.Message)
	case http.StatusBadRequest:
		return fmt.Errorf("bad request: %s", errResp.Error.Message)
	default:
		return fmt.Errorf("HTTP %d: %s", statusCode, errResp.Error.Message)
	}
}
