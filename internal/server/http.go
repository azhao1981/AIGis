package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"aigis/internal/adminui"
	"aigis/internal/config"
	"aigis/internal/core"
	"aigis/internal/core/audit"
	"aigis/internal/core/breaker"
	"aigis/internal/core/cache"
	"aigis/internal/core/engine"
	"aigis/internal/core/limiter"
	"aigis/internal/core/metrics"
	"aigis/internal/core/providers"
	"aigis/internal/core/quota"
	"aigis/internal/core/security"
	"aigis/internal/core/transform"
	"aigis/internal/core/usage"
	"aigis/internal/pkg/logger"
)

// HTTPServer extends the basic server with gateway functionality
type HTTPServer struct {
	*Server
	engine   *engine.Engine
	mux      *http.ServeMux
	logger   *logger.Logger
	auditor  *audit.Auditor
	metrics  *metrics.Metrics
	scanner  *security.Scanner           // shared, built once with built-in + custom rules
	limiter  *limiter.ConcurrencyLimiter // global in-flight cap (no-op when unconfigured)
	breakers *breaker.Set                // per-route circuit breakers (no-op when disabled)
	cache    *cache.TTLCache             // non-streaming response cache (no-op when disabled)
	retry    providers.RetryPolicy       // upstream retry policy (zero value = no retry)

	// usageSink receives one usage.Event per request. Defaults to usage.NopSink
	// in the open-source build; the Enterprise Edition injects a metering/billing
	// sink via SetUsageSink(). Never nil.
	usageSink usage.Sink

	// quota gates requests per tenant after auth. Defaults to quota.AllowAll (no
	// rejection) in the open-source build; the Enterprise Edition injects a
	// per-tenant limiter via SetQuotaLimiter(). Never nil.
	quota quota.Limiter

	// middlewares wrap the mux (auth, quota, ...). Empty in the open-source build;
	// the Enterprise Edition registers its own via Use(). See middleware.go.
	middlewares []Middleware
}

// auditLogPath is the on-disk JSONL audit trail of masked sensitive info.
// Hardcoded like logger.logFile (path is not worth a config knob; the on/off
// toggle lives in config `audit.enabled`).
const auditLogPath = "./logs/audit.jsonl"

// NewHTTPServer creates a new HTTP server with gateway capabilities
func NewHTTPServer(addr string, zapLogger *zap.Logger) (*HTTPServer, error) {
	baseServer := New(addr)

	// Wrap zap logger with our extension
	extLogger := logger.NewLogger(zapLogger)

	// Load engine configuration
	engineConfig, err := config.LoadEngineConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load engine config: %w", err)
	}

	// Validate config before building the engine so a bad config fails loud at
	// startup instead of misrouting (or panicking) on a live request.
	if err := engineConfig.Validate(transform.KnownTypes(), transform.KnownStreamTranslators()); err != nil {
		return nil, fmt.Errorf("invalid engine config: %w", err)
	}

	// Create transformation engine
	eng, err := engine.NewEngine(engineConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create engine: %w", err)
	}

	extLogger.Info("Engine initialized",
		zap.Int("routes", len(engineConfig.Routes)),
	)

	// Log configured routes
	for _, route := range engineConfig.Routes {
		extLogger.Info("Route configured",
			zap.String("id", route.ID),
			zap.String("upstream", route.Upstream.ResolvedBaseURL()),
			zap.Int("transforms", len(route.Transforms)),
		)
	}

	// Audit logger for masked sensitive info (JSONL; previews are partially masked).
	auditEnabled := config.AuditEnabled()
	auditor, err := audit.New(auditLogPath, auditEnabled, zapLogger)
	if err != nil {
		return nil, fmt.Errorf("failed to create auditor: %w", err)
	}
	extLogger.Info("Audit logger initialized",
		zap.Bool("enabled", auditEnabled),
		zap.String("path", auditLogPath),
	)

	// Build the shared scanner once (built-in rules + user custom_rules). Custom
	// regexes are compiled and validated here, so a bad rule fails loud at startup.
	customRules, err := config.LoadCustomRules()
	if err != nil {
		return nil, fmt.Errorf("failed to load custom rules: %w", err)
	}
	scanner, err := security.NewScannerWithRules(customRules)
	if err != nil {
		return nil, fmt.Errorf("invalid custom rule: %w", err)
	}
	extLogger.Info("Security scanner initialized",
		zap.Int("custom_rules", len(customRules)),
	)

	maxConcurrent := config.MaxConcurrent()
	extLogger.Info("Concurrency limiter initialized",
		zap.Int("max_concurrent", maxConcurrent), // 0 = unlimited
	)

	breakerCfg := config.BreakerConfig()
	extLogger.Info("Circuit breaker initialized",
		zap.Bool("enabled", breakerCfg.Enabled),
		zap.Int("fail_threshold", breakerCfg.FailThreshold),
		zap.Duration("cooldown", breakerCfg.Cooldown),
	)

	cacheTTL := config.CacheTTL()
	extLogger.Info("Response cache initialized",
		zap.Duration("ttl", cacheTTL), // 0 = disabled
		zap.Int("max_entries", config.CacheMaxEntries()),
	)

	retryPolicy := config.RetryConfig()
	extLogger.Info("Upstream retry initialized",
		zap.Int("max_attempts", retryPolicy.MaxAttempts), // 1 = no retry
		zap.Duration("backoff", retryPolicy.Backoff),
	)

	s := &HTTPServer{
		Server:    baseServer,
		engine:    eng,
		logger:    extLogger,
		auditor:   auditor,
		metrics:   metrics.New(),
		scanner:   scanner,
		limiter:   limiter.New(maxConcurrent),
		breakers:  breaker.NewSet(breakerCfg),
		cache:     cache.New(cacheTTL, config.CacheMaxEntries()),
		retry:     retryPolicy,
		usageSink: usage.NopSink{},
		quota:     quota.AllowAll(),
	}

	// Initialize mux
	s.mux = s.setupRoutes()

	return s, nil
}

// SetUsageSink replaces the per-request usage sink. The open-source build uses
// usage.NopSink; the Enterprise Edition calls this from cmd/aigis-ee to plug in
// a metering/billing sink. A nil sink resets to the no-op. Call before Start().
func (s *HTTPServer) SetUsageSink(sink usage.Sink) {
	if sink == nil {
		sink = usage.NopSink{}
	}
	s.usageSink = sink
}

// SetQuotaLimiter replaces the per-tenant quota limiter. The open-source build
// uses quota.AllowAll (never rejects); the Enterprise Edition calls this from
// cmd/aigis-ee to enforce per-tenant limits. A nil limiter resets to allow-all.
// Call before Start().
func (s *HTTPServer) SetQuotaLimiter(q quota.Limiter) {
	if q == nil {
		q = quota.AllowAll()
	}
	s.quota = q
}

// setupRoutes creates and configures the HTTP routes
func (s *HTTPServer) setupRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	// Health / readiness endpoint: aggregates liveness with each route's circuit
	// state so a load balancer or k8s readiness probe can see degradation.
	mux.HandleFunc("/health", s.handleHealth)

	// Concurrency metrics endpoint: current in-flight, peak, and cumulative totals.
	mux.HandleFunc("/metrics", s.handleMetrics)

	// Read-only route config view for the dashboard's Routes panel. Exposes only
	// structural config (never token values); same tier as /health and /metrics.
	mux.HandleFunc("/admin/routes-info", s.handleRoutes)

	// Gateway endpoints for LLM requests.
	// Both share one handler; the engine routes by model regardless of inbound path.
	// /v1/chat/completions: OpenAI-compatible clients
	// /v1/messages: Anthropic-native clients (e.g. Claude Code via ANTHROPIC_BASE_URL)
	mux.HandleFunc("/v1/chat/completions", s.handleGateway)
	mux.HandleFunc("/v1/messages", s.handleGateway)

	// Admin dashboard: embedded single page + capability discovery. Core ships
	// only the status panel; EE lights up more panels via a capabilities
	// middleware layered on /ui/capabilities.
	adminui.RegisterRoutes(mux)

	// Root endpoint
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":"AIGis is running"}`))
	})

	return mux
}

// Handler returns the HTTP handler (mux wrapped by any registered middlewares)
// for testing and embedding.
func (s *HTTPServer) Handler() http.Handler {
	return s.buildHandler()
}

// Start starts the HTTP server with gateway endpoints
func (s *HTTPServer) Start() error {
	s.server = &http.Server{
		Addr:         s.addr,
		Handler:      s.buildHandler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		s.logger.Info("Starting AIGis", zap.String("addr", s.addr))
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Fatal("Server error", zap.Error(err))
		}
	}()

	<-stop
	s.logger.Skip(0).Info("Shutting down server...")

	if err := s.auditor.Close(); err != nil {
		s.logger.Error("Failed to close auditor", zap.Error(err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return s.server.Shutdown(ctx)
}

// handleGateway processes LLM requests through the engine.
// Serves both /v1/chat/completions (OpenAI) and /v1/messages (Anthropic);
// routing is decided by the engine from the request body's model field.
func (s *HTTPServer) handleGateway(w http.ResponseWriter, r *http.Request) {
	// Only accept POST requests
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Admission control: reject when the global in-flight ceiling is reached
	// (no-op when limit.max_concurrent is 0). Done before any work so an
	// overload sheds load cheaply. Released on every return path.
	if !s.limiter.Acquire() {
		s.logger.Warn("Rate limited: max concurrency reached",
			zap.Int64("in_flight", s.limiter.InFlight()),
		)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Too many requests", http.StatusTooManyRequests)
		return
	}
	defer s.limiter.Release()

	// Concurrency monitoring: count this request as in-flight for its whole
	// lifetime. succeeded defaults to false so any early-return error path tallies
	// as failed; the success paths flip it to true. (Named succeeded, not ok, to
	// avoid shadowing by the `flusher, ok :=` type assertion below.)
	s.metrics.Begin()
	succeeded := false
	defer func() { s.metrics.End(succeeded) }()

	// Read the raw body into []byte
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Error("Failed to read body", zap.Error(err))
		http.Error(w, fmt.Sprintf("Failed to read body: %v", err), http.StatusBadRequest)
		return
	}

	// Generate request and trace IDs
	requestID := generateRequestID()
	traceID := uuid.New().String()

	// Create a logger with request context
	reqLogger := s.logger.With(
		zap.String("request_id", requestID),
		zap.String("trace_id", traceID),
	)

	// Adopt the authenticated tenant, if any. In the open-source build no inbound
	// auth runs, so this is empty; the Enterprise auth middleware stashes it via
	// core.WithTenant so logging, audit, and quota can be scoped per tenant.
	if id, ok := core.TenantFromContext(r.Context()); ok {
		reqLogger = reqLogger.With(
			zap.String("tenant", id.Tenant),
			zap.String("subject", id.Subject),
		)
	}

	// Create a GatewayContext
	ctx := core.NewGatewayContext(r.Context(), reqLogger.Logger)
	ctx.RequestID = requestID
	ctx.TraceID = traceID
	if id, ok := core.TenantFromContext(r.Context()); ok {
		ctx.Tenant = id.Tenant
		ctx.Subject = id.Subject
	}

	// Per-tenant quota gate (after auth, before any upstream work). No-op in the
	// open-source build (quota.AllowAll); the Enterprise limiter rejects a tenant
	// that is over its ceiling with 429. Release frees the slot at request end.
	if release, ok := s.quota.Acquire(ctx.Tenant); ok {
		defer release()
	} else {
		reqLogger.Warn("Tenant quota exceeded", zap.String("tenant", ctx.Tenant))
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Tenant quota exceeded", http.StatusTooManyRequests)
		return
	}

	// Audit masked sensitive info at request end (metadata only; no-op if nothing
	// was masked). Covers both streaming and blocking paths via a single defer.
	defer s.auditor.Record(ctx)

	// Usage metering: emit exactly one usage.Event per request at the end,
	// regardless of exit path. Fields captured by closure are filled in as the
	// request progresses (route, streaming intent, token counts from the upstream
	// response). No-op in the open-source build (usage.NopSink).
	usageEvt := usage.Event{
		Tenant:    ctx.Tenant,
		Subject:   ctx.Subject,
		RequestID: requestID,
	}
	defer func() {
		usageEvt.Success = succeeded
		usageEvt.DurationMS = time.Since(ctx.StartTime).Milliseconds()
		s.usageSink.Record(r.Context(), usageEvt)
	}()

	// Log request completion with latency on every exit path (streaming included),
	// reflecting the final success/failure outcome.
	defer func() {
		status := "Success"
		if !succeeded {
			status = "Failed"
		}
		reqLogger.Info("Request finished",
			zap.Float64("latency_ms", float64(time.Since(ctx.StartTime).Microseconds())/1000),
			zap.String("status", status),
		)
	}()

	reqLogger.Info("Request started",
		zap.String("method", r.Method),
		zap.String("path", r.URL.Path),
		zap.String("model", gjson.GetBytes(body, "model").String()),
	)

	// Find matching route using engine
	route, err := s.engine.FindRoute(body)
	if err != nil {
		reqLogger.Error("Route matching error", zap.Error(err))
		http.Error(w, fmt.Sprintf("Route matching error: %v", err), http.StatusBadRequest)
		return
	}

	if route == nil {
		reqLogger.Warn("No matching route found")
		http.Error(w, "No matching route configured", http.StatusNotFound)
		return
	}

	reqLogger.Info("Route matched",
		zap.String("route_id", route.ID),
		zap.String("upstream", route.Upstream.ResolvedBaseURL()),
	)

	// Stash routing metadata for the audit record (read in the deferred Record).
	ctx.SetMetadata("route_id", route.ID)
	ctx.SetMetadata("model", gjson.GetBytes(body, "model").String())

	// Streaming intent (clients set "stream": true to request SSE).
	isStream := gjson.GetBytes(body, "stream").Bool()

	usageEvt.RouteID = route.ID
	usageEvt.Model = gjson.GetBytes(body, "model").String()
	usageEvt.Streamed = isStream

	// Response cache (non-streaming only): serve an identical recent request
	// without hitting the upstream. Checked BEFORE the breaker so a cache hit is
	// neither blocked by an open circuit nor consumes a half-open probe slot.
	var cacheKey string
	if !isStream {
		cacheKey = cache.Key(r.URL.Path, string(body))
		if cached, ok := s.cache.Get(cacheKey); ok {
			reqLogger.Info("Cache hit", zap.String("route_id", route.ID))
			w.Header().Set("X-Cache", "HIT")
			w.Header().Set("Content-Type", "application/json")
			w.Write(cached)
			applyUsageTokens(&usageEvt, cached)
			succeeded = true
			return
		}
	}

	// Circuit breaker: if this route's upstream is currently tripped open, fail
	// fast with 503 instead of piling onto a sick backend (no-op when disabled).
	br := s.breakers.Get(route.ID)
	if !br.Allow() {
		reqLogger.Warn("Circuit open, failing fast", zap.String("route_id", route.ID))
		w.Header().Set("Retry-After", "5")
		http.Error(w, "Upstream temporarily unavailable (circuit open)", http.StatusServiceUnavailable)
		return
	}

	// Create universal provider for this route
	provider := providers.NewUniversalProvider(route, reqLogger, s.scanner).WithRetry(s.retry)

	// Branch on streaming: the flusher must be available to stream; otherwise
	// fall back to blocking. A force_block route is deliberately kept OUT of the
	// real streaming branch — it must go through the blocking Send so the
	// pre-send egress leak check runs on the fully-masked body before anything
	// leaves the gateway; its client still gets SSE via a pseudo-stream below.
	if flusher, ok := w.(http.Flusher); isStream && ok && !route.ForceBlock {
		// SSE response headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Stream upstream SSE through, unmasking placeholders per chunk.
		// Pass the AIGisContext (ctx) for bidirectional tokenization (vault).
		if err := provider.SendStream(ctx, body, r.Header, w, flusher); err != nil {
			// If nothing has been written yet the status is still settable; otherwise
			// the error is logged and the stream simply ends.
			reqLogger.Error("Provider stream error", zap.Error(err))
			br.RecordFailure()
		} else {
			succeeded = true
			br.RecordSuccess()
		}
		return
	}

	// Strict-review pseudo-stream: the client asked for SSE and force_block is on,
	// so instead of real streaming we run the blocking Send (which performs the
	// pre-send egress leak check on the fully-masked body), then re-emit the
	// single buffered response as one SSE data event followed by [DONE]. The
	// client is unaware it was internally a blocking call.
	if flusher, ok := w.(http.Flusher); isStream && ok && route.ForceBlock {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		resp, err := provider.Send(ctx, body, r.Header)
		if err != nil {
			// Egress-blocked or upstream error: nothing has been written yet, so a
			// clean SSE error event is still deliverable before the stream ends.
			br.RecordFailure()
			reqLogger.Error("Provider error (force_block stream)", zap.Error(err))
			fmt.Fprintf(w, "data: {\"error\":{\"message\":%q}}\n\n", err.Error())
			flusher.Flush()
			return
		}
		br.RecordSuccess()

		// Emit the buffered result as a single SSE chunk, then the terminator.
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: %s\n\n", resp)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		applyUsageTokens(&usageEvt, resp)
		succeeded = true
		return
	}

	// Non-streaming path
	w.Header().Set("Content-Type", "application/json")

	// Send request through provider (includes transforms and header handling)
	// Pass the AIGisContext (ctx) instead of r.Context() for bidirectional tokenization
	resp, err := provider.Send(ctx, body, r.Header)
	if err != nil {
		br.RecordFailure()
		reqLogger.Error("Provider error", zap.Error(err))
		http.Error(w, fmt.Sprintf("Provider error: %v", err), http.StatusBadGateway)
		return
	}
	br.RecordSuccess()

	// Cache the fresh response for identical future (non-streaming) requests.
	if cacheKey != "" {
		s.cache.Set(cacheKey, resp)
		w.Header().Set("X-Cache", "MISS")
	}

	// Return the upstream response.
	w.WriteHeader(http.StatusOK)
	w.Write(resp)
	applyUsageTokens(&usageEvt, resp)
	succeeded = true
}

// applyUsageTokens fills the token counts on a usage.Event from an upstream
// response body, tolerating both the OpenAI shape ("usage.prompt_tokens",
// "usage.completion_tokens", "usage.total_tokens") and the Anthropic shape
// ("usage.input_tokens", "usage.output_tokens"). Missing fields stay 0.
func applyUsageTokens(e *usage.Event, resp []byte) {
	prompt := gjson.GetBytes(resp, "usage.prompt_tokens")
	if !prompt.Exists() {
		prompt = gjson.GetBytes(resp, "usage.input_tokens")
	}
	completion := gjson.GetBytes(resp, "usage.completion_tokens")
	if !completion.Exists() {
		completion = gjson.GetBytes(resp, "usage.output_tokens")
	}
	e.PromptTokens = int(prompt.Int())
	e.CompletionTokens = int(completion.Int())
	if total := gjson.GetBytes(resp, "usage.total_tokens"); total.Exists() {
		e.TotalTokens = int(total.Int())
	} else {
		e.TotalTokens = e.PromptTokens + e.CompletionTokens
	}
}

// handleHealth reports liveness plus per-route circuit state. It always returns
// HTTP 200 (the process is alive and can still serve routes whose upstreams are
// healthy); the body's "status" is "degraded" when any route's breaker is not
// closed, so a readiness probe / dashboard can surface the degradation without
// taking the whole gateway out of rotation. Breaker states only appear when the
// breaker is enabled (otherwise all routes are trivially "closed").
func (s *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	routes := map[string]string{}
	degraded := false
	for _, route := range s.engine.GetConfig().Routes {
		state := s.breakers.Get(route.ID).State().String()
		routes[route.ID] = state
		if state != "closed" {
			degraded = true
		}
	}

	status := "ok"
	if degraded {
		status = "degraded"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"status": status,
		"routes": routes,
	}); err != nil {
		s.logger.Error("Failed to encode health", zap.Error(err))
	}
}

// handleRoutes returns a read-only, sanitized view of the configured routes for
// the admin dashboard's Routes panel: each route's matcher, upstream shape, auth
// strategy, transform types, force_block/stream_translate, and live breaker
// state, plus the global retry policy. It NEVER reads or emits any token VALUE —
// only the env variable NAME(s) are exposed (token_env / token_envs), so a
// secret can never leak through this endpoint. No auth is required (same tier as
// /health and /metrics); it exposes only structural config, never credentials.
func (s *HTTPServer) handleRoutes(w http.ResponseWriter, r *http.Request) {
	type upstreamInfo struct {
		BaseURL       string   `json:"base_url"`
		Path          string   `json:"path"`
		AuthStrategy  string   `json:"auth_strategy"`
		TokenEnv      string   `json:"token_env,omitempty"`
		TokenEnvs     []string `json:"token_envs,omitempty"`
		TokenKeyCount int      `json:"token_key_count"`
	}
	type routeInfo struct {
		ID              string            `json:"id"`
		Matcher         map[string]string `json:"matcher"`
		Upstream        upstreamInfo      `json:"upstream"`
		Transforms      []string          `json:"transforms"`
		ForceBlock      bool              `json:"force_block"`
		StreamTranslate string            `json:"stream_translate,omitempty"`
		BreakerState    string            `json:"breaker_state"`
	}

	var routes []routeInfo
	for _, route := range s.engine.GetConfig().Routes {
		up := route.Upstream
		keyCount := len(up.TokenEnvs)
		if keyCount == 0 && up.TokenEnv != "" {
			keyCount = 1
		}
		transforms := make([]string, 0, len(route.Transforms))
		for _, t := range route.Transforms {
			transforms = append(transforms, t.Type)
		}
		routes = append(routes, routeInfo{
			ID:      route.ID,
			Matcher: route.Matcher,
			Upstream: upstreamInfo{
				BaseURL:       up.ResolvedBaseURL(),
				Path:          up.Path,
				AuthStrategy:  up.AuthStrategy,
				TokenEnv:      up.TokenEnv,
				TokenEnvs:     up.TokenEnvs,
				TokenKeyCount: keyCount,
			},
			Transforms:      transforms,
			ForceBlock:      route.ForceBlock,
			StreamTranslate: route.StreamTranslate,
			BreakerState:    s.breakers.Get(route.ID).State().String(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"retry": map[string]any{
			"max_attempts": s.retry.MaxAttempts,
			"backoff_ms":   s.retry.Backoff.Milliseconds(),
		},
		"routes": routes,
	}); err != nil {
		s.logger.Error("Failed to encode routes", zap.Error(err))
	}
}

// handleMetrics returns a JSON snapshot of concurrency counters for monitoring.
func (s *HTTPServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	snap := s.metrics.Snapshot()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(snap); err != nil {
		s.logger.Error("Failed to encode metrics", zap.Error(err))
	}
}

// generateRequestID generates a simple request ID for tracking
func generateRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}
