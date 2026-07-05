package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"

	"aigis/internal/core"
	"aigis/internal/core/engine"
	"aigis/internal/core/security"
	"aigis/internal/core/transform"
	"aigis/internal/pkg/logger"
)

// RetryPolicy configures upstream retry on transient failures. MaxAttempts <= 1
// disables retry (a single try), preserving the default behavior. Only network
// errors and 429/5xx responses are retried (see isRetryable); a 4xx is a client
// error and is returned immediately.
type RetryPolicy struct {
	MaxAttempts int           // total tries including the first (1 = no retry)
	Backoff     time.Duration // base backoff; grows linearly per attempt
}

// TransformError marks a request rejected by a request-side transform (e.g.
// injection block, guard budget): the fault is the client's and the upstream
// was never called, so the gateway maps it to 400 and skips breaker bookkeeping.
// Transform carries the strategy name ("injection", "guard", "pii", ...) so the
// handler can attribute the rejection in metrics without parsing error text.
type TransformError struct {
	Transform string
	Err       error
}

func (e *TransformError) Error() string {
	return "transform error: " + e.Err.Error()
}
func (e *TransformError) Unwrap() error { return e.Err }

// UniversalProvider implements the core.Provider interface with configurable routing
type UniversalProvider struct {
	route    *engine.Route
	client   *http.Client
	scanner  *security.Scanner   // used for streaming line-by-line unmask
	registry *transform.Registry // pluggable request/response transformers
	log      *logger.Logger
	retry    RetryPolicy // upstream retry policy (zero value = no retry)
}

// rrCursors holds the multi-key round-robin cursor per route ID. A new
// UniversalProvider is built for every request (see handleGateway), so the
// cursor cannot live on the provider or it would reset each time; it is keyed by
// the stable route ID here so the rotation persists across requests.
var rrCursors sync.Map // route ID -> *uint64

// nextRRIndex atomically advances and returns the round-robin index for a route.
func nextRRIndex(routeID string, n int) int {
	v, _ := rrCursors.LoadOrStore(routeID, new(uint64))
	c := v.(*uint64)
	return int((atomic.AddUint64(c, 1) - 1) % uint64(n))
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

// WithRetry sets the upstream retry policy and returns the provider for
// chaining. A zero/MaxAttempts<=1 policy leaves retry off (the default).
func (p *UniversalProvider) WithRetry(policy RetryPolicy) *UniversalProvider {
	p.retry = policy
	return p
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
		return nil, wrapTransformError(err)
	}

	// Step 1b: Strict-review egress check. On force_block routes, scan the
	// fully-masked body one last time before anything leaves the gateway; if a
	// built-in secret pattern still matches, a masking rule missed it — refuse to
	// send rather than leak.
	if err := p.egressLeakCheck(transformedBody); err != nil {
		return nil, err
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
		return wrapTransformError(err)
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

// egressLeakCheck is the final pre-send safety net for strict-review routes.
// It is a no-op unless the route sets force_block. It scans the already-masked
// body with the scanner's built-in secret rules; any match means masking missed
// a secret, so it returns an error to abort the request before it egresses.
// Built-in rules only: route-scoped custom rules mark business IDs (e.g. order
// numbers), not leak-grade secrets, so their residue must not block a request.
func (p *UniversalProvider) egressLeakCheck(body []byte) error {
	if !p.route.ForceBlock {
		return nil
	}
	if hits := p.scanner.Detect(string(body), nil); len(hits) > 0 {
		p.log.Warn("Egress leak check blocked request",
			zap.String("route_id", p.route.ID),
			zap.Strings("leaked_rules", hits),
		)
		return fmt.Errorf("egress blocked: sensitive data (%s) survived masking", strings.Join(hits, ", "))
	}
	return nil
}

// applyRequestTransforms applies all configured transformations to the request
// body by dispatching each step to its registered Transformer (Strategy).
// On failure, the offending transform name is reported via the returned error
// chain so callers (handler / metrics) can attribute the rejection.
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
			return nil, &transformError{transform: step.Type, err: err}
		}
	}

	return result, nil
}

// transformError is the internal, transform-attributed error. Send/SendStream
// unwrap it into the public TransformError (see Send / SendStream).
type transformError struct {
	transform string
	err       error
}

func (e *transformError) Error() string {
	return "transform " + e.transform + " failed: " + e.err.Error()
}
func (e *transformError) Unwrap() error { return e.err }

// wrapTransformError converts the internal transformError into the public
// TransformError (preserving the strategy name). A non-transform error is
// returned as-is.
func wrapTransformError(err error) error {
	var te *transformError
	if errors.As(err, &te) {
		return &TransformError{Transform: te.transform, Err: te.err}
	}
	return err
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

// selectToken resolves the upstream token, round-robining across TokenEnvs when
// multi-key load balancing is configured. It skips env vars that resolve to an
// empty value so a missing key never becomes an empty Authorization header. When
// TokenEnvs is empty (or none resolve), it falls back to the single TokenEnv.
func (p *UniversalProvider) selectToken() string {
	envs := p.route.Upstream.TokenEnvs
	if len(envs) > 0 {
		var keys []string
		for _, name := range envs {
			if v := os.Getenv(name); v != "" {
				keys = append(keys, v)
			}
		}
		if len(keys) > 0 {
			return keys[nextRRIndex(p.route.ID, len(keys))]
		}
	}
	return os.Getenv(p.route.Upstream.TokenEnv)
}

// buildAuthHeaders constructs authentication headers based on the route's AuthStrategy
func (p *UniversalProvider) buildAuthHeaders() http.Header {
	headers := make(http.Header)
	upstream := p.route.Upstream

	token := p.selectToken()
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
	baseURL := upstream.ResolvedBaseURL()

	// Build URL
	path := upstream.Path
	if path == "" {
		path = "/chat/completions" // Default for OpenAI compatibility
	}
	url := baseURL + path

	// Handle query params for AuthStrategyQuery
	if upstream.AuthStrategy == engine.AuthStrategyQuery {
		token := p.selectToken()
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

// sendToUpstream sends the transformed request to the upstream, retrying on
// transient failures per the retry policy. Retries only cover network errors
// and 429/5xx responses (isRetryable); a 4xx returns immediately. With the
// default policy (MaxAttempts <= 1) this is a single attempt.
func (p *UniversalProvider) sendToUpstream(ctx context.Context, body []byte, originalHeaders http.Header) ([]byte, error) {
	attempts := p.retry.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	var respBody []byte
	var status int
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		respBody, status, err = p.doOnce(ctx, body, originalHeaders)
		if err == nil {
			return respBody, nil
		}
		// Stop early on a non-retryable failure or after the last attempt.
		if attempt == attempts || !isRetryable(status) {
			break
		}
		wait := p.retry.Backoff * time.Duration(attempt)
		p.log.Warn("Upstream attempt failed, retrying",
			zap.String("route_id", p.route.ID),
			zap.Int("attempt", attempt),
			zap.Int("status", status),
			zap.Duration("backoff", wait),
			zap.Error(err),
		)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, err
}

// doOnce performs a single upstream call and returns the response body, the
// HTTP status (0 on a transport-level error), and an error. On a non-200
// status the error carries handleHTTPError's message; status is returned so the
// caller can decide whether to retry.
func (p *UniversalProvider) doOnce(ctx context.Context, body []byte, originalHeaders http.Header) ([]byte, int, error) {
	httpReq, err := p.buildUpstreamRequest(ctx, body, originalHeaders)
	if err != nil {
		return nil, 0, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, p.handleHTTPError(resp.StatusCode, respBody)
	}

	return respBody, resp.StatusCode, nil
}

// isRetryable reports whether an upstream failure with the given status is worth
// retrying: a transport error (status 0), 429 (rate limited), or any 5xx. A 4xx
// (other than 429) is a client error that a retry won't fix.
func isRetryable(status int) bool {
	return status == 0 || status == http.StatusTooManyRequests || status >= 500
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
