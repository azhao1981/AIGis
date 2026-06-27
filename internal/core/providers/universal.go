package providers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/bytedance/sonic"

	"aigis/internal/core"
	"aigis/internal/core/engine"
	"aigis/internal/core/security"
	"aigis/internal/core/transform"
	"aigis/internal/pkg/logger"
)

// UniversalProvider implements the core.Provider interface with configurable routing
type UniversalProvider struct {
	route    *engine.Route
	client   *http.Client
	scanner  *security.Scanner   // used for streaming line-by-line unmask
	registry *transform.Registry // pluggable request/response transformers
	log      *logger.Logger
}

// NewUniversalProvider creates a new universal provider for the given route.
// The scanner is injected (shared, built once at startup with any custom rules);
// a nil scanner falls back to a default one with only built-in rules (tests).
func NewUniversalProvider(route *engine.Route, log *logger.Logger, scanner *security.Scanner) *UniversalProvider {
	if log == nil {
		// Create a default logger if none provided
		zapLogger, _ := logger.New("info")
		log = logger.NewLogger(zapLogger)
	}
	if scanner == nil {
		scanner = security.NewScanner()
	}
	return &UniversalProvider{
		route:    route,
		scanner:  scanner,
		registry: transform.NewDefaultRegistry(scanner),
		log:      log,
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

// SendStream sends a request and streams the upstream SSE response through to w,
// unmasking placeholders line-by-line and flushing after each write.
// Request transforms (PII masking) are applied identically to the non-streaming path.
func (p *UniversalProvider) SendStream(ctx *core.AIGisContext, body []byte, originalHeaders http.Header, w io.Writer, flusher http.Flusher) error {
	// Step 1: Apply request transforms (with bidirectional tokenization)
	transformedBody, err := p.applyRequestTransforms(ctx, body)
	if err != nil {
		return fmt.Errorf("transform error: %w", err)
	}

	// Step 2: Build and send the upstream request, keeping the body stream open
	httpReq, err := p.buildUpstreamRequest(ctx.Context, transformedBody, originalHeaders)
	if err != nil {
		return err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Upstream error before streaming any bytes: surface as a normal error
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return p.handleHTTPError(resp.StatusCode, errBody)
	}

	// Step 3: Stream the SSE response, restoring tokenized secrets. The
	// transformer is chosen by the route's stream_translate: the default
	// StreamUnmasker passes events through (handling placeholders split across
	// SSE text deltas), while e.g. "dify" translates Dify SSE into OpenAI
	// chunks. An unknown name is rejected at config validation; fall back to the
	// passthrough unmasker defensively here.
	st, ok := transform.NewStreamTransformer(p.route.StreamTranslate, p.scanner, ctx)
	if !ok {
		st = transform.NewStreamUnmasker(p.scanner, ctx)
	}
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			out, _ := st.Write(buf[:n])
			if len(out) > 0 {
				if _, wErr := w.Write(out); wErr != nil {
					return fmt.Errorf("failed to write stream: %w", wErr)
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
		}
		if readErr != nil {
			// Emit any buffered remainder before ending the stream.
			if out, _ := st.Flush(); len(out) > 0 {
				w.Write(out)
				if flusher != nil {
					flusher.Flush()
				}
			}
			if readErr == io.EOF {
				return nil
			}
			return fmt.Errorf("failed to read upstream stream: %w", readErr)
		}
	}
}

// applyRequestTransforms applies all configured transformations to the request
// body by dispatching each step to its registered Transformer (Strategy).
func (p *UniversalProvider) applyRequestTransforms(ctx *core.AIGisContext, body []byte) ([]byte, error) {
	result := body

	for _, step := range p.route.Transforms {
		t, ok := p.registry.Get(step.Type)
		if !ok {
			// Unknown transform type, skip (preserves prior behavior)
			continue
		}
		var err error
		if result, err = t.Apply(ctx, result, step.Config); err != nil {
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

// applyResponseTransforms reshapes the (non-streaming) response body: first any
// route-configured response transforms (e.g. Dify -> OpenAI shape via template),
// then unmask to restore tokenized secrets last.
func (p *UniversalProvider) applyResponseTransforms(ctx *core.AIGisContext, body []byte) ([]byte, error) {
	result := body

	// 1. Route-configured response transforms (protocol translation, etc.)
	for _, step := range p.route.ResponseTransforms {
		t, ok := p.registry.Get(step.Type)
		if !ok {
			// Unknown transform type, skip (consistent with request path)
			continue
		}
		var err error
		if result, err = t.Apply(ctx, result, step.Config); err != nil {
			return nil, fmt.Errorf("response transform %s failed: %w", step.Type, err)
		}
	}

	// 2. Always unmask tokenized secrets last so restored content survives any
	//    reshaping above.
	if t, ok := p.registry.Get(transform.TypeUnmask); ok {
		return t.Apply(ctx, result, nil)
	}
	return result, nil
}

// buildUpstreamRequest constructs the upstream HTTP request (URL, env:VAR resolution,
// query-auth, and HeaderPolicy/auth headers). Shared by blocking and streaming paths.
func (p *UniversalProvider) buildUpstreamRequest(ctx context.Context, body []byte, originalHeaders http.Header) (*http.Request, error) {
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

	return httpReq, nil
}

// sendToUpstream sends the transformed request to the upstream service with header handling
func (p *UniversalProvider) sendToUpstream(ctx context.Context, body []byte, originalHeaders http.Header) ([]byte, error) {
	httpReq, err := p.buildUpstreamRequest(ctx, body, originalHeaders)
	if err != nil {
		return nil, err
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
