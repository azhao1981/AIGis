package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
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

	// scannerMu guards scanner swaps during config reloads. The read side
	// (every masked request) takes RLock to fetch the current pointer; reload
	// takes Lock to swap in a freshly-built one. nil-safe: NewHTTPServer
	// always installs a non-nil scanner.
	scannerMu sync.RWMutex

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

// currentScanner returns the live scanner under the reload RLock. Every gateway
// request goes through here so a config reload can swap the scanner without
// restarting the process (see ReloadConfig).
func (s *HTTPServer) currentScanner() *security.Scanner {
	s.scannerMu.RLock()
	defer s.scannerMu.RUnlock()
	return s.scanner
}

// ReloadConfig hot-swaps the routing engine and the security scanner from a
// fresh viper read. Triggered by SIGHUP (see cmd/aigis/serve.go) so route /
// custom-rule edits apply without restart. Scope is deliberately limited to
// engine.routes + security.custom_rules — other knobs (log/limit/breaker/cache/
// retry/audit) still need a restart to keep the surface small and predictable.
//
// Fail loud: on any validation error (bad regex, bad custom rule, missing
// engine section) the live config is left UNTOUCHED and the error returned, so
// a malformed edit can never break a running gateway.
func (s *HTTPServer) ReloadConfig() error {
	newEngineCfg, err := config.LoadEngineConfig()
	if err != nil {
		return fmt.Errorf("reload: failed to load engine config: %w", err)
	}
	if err := newEngineCfg.Validate(transform.KnownTypes(), transform.KnownStreamTranslators()); err != nil {
		return fmt.Errorf("reload: invalid engine config: %w", err)
	}
	customRules, err := config.LoadCustomRules()
	if err != nil {
		return fmt.Errorf("reload: failed to load custom rules: %w", err)
	}
	newScanner, err := security.NewScannerWithRules(customRules)
	if err != nil {
		return fmt.Errorf("reload: invalid custom rule: %w", err)
	}

	if err := s.engine.Reload(newEngineCfg); err != nil {
		return fmt.Errorf("reload: engine swap failed: %w", err)
	}

	s.scannerMu.Lock()
	s.scanner = newScanner
	s.scannerMu.Unlock()

	s.logger.Info("Configuration reloaded",
		zap.Int("routes", len(newEngineCfg.Routes)),
		zap.Int("custom_rules", len(customRules)),
	)
	return nil
}

// setupRoutes creates and configures the HTTP routes
func (s *HTTPServer) setupRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	// Health / readiness endpoint: aggregates liveness with each route's circuit
	// state so a load balancer or k8s readiness probe can see degradation.
	mux.HandleFunc("/health", s.handleHealth)

	// Concurrency metrics endpoint: current in-flight, peak, and cumulative totals.
	mux.HandleFunc("/metrics", s.handleMetrics)

	// Prometheus text-format view of the same counters plus per-route breaker
	// state. Hand-rolled exposition (no client library dependency).
	mux.HandleFunc("/metrics/prometheus", s.handlePrometheus)

	// Read-only route config view for the dashboard's Routes panel. Exposes only
	// structural config (never token values); same tier as /health and /metrics.
	mux.HandleFunc("/admin/routes-info", s.handleRoutes)

	// Read-only view of the masking audit trail for the dashboard's Masking
	// panel. Serves the tail of ./logs/audit.jsonl — metadata only (rule types,
	// counts, placeholders, partially-masked previews), never plaintext.
	mux.HandleFunc("/admin/audit", s.handleAudit)

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

	// Request/trace IDs: honor an inbound X-Request-ID (so callers can correlate
	// their own logs with the gateway's), generate one otherwise, and always echo
	// it back on the response so every reply is traceable end to end.
	requestID := sanitizeRequestID(r.Header.Get("X-Request-ID"))
	if requestID == "" {
		requestID = generateRequestID()
	}
	traceID := uuid.New().String()
	w.Header().Set("X-Request-ID", requestID)

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
	// reflecting the final success/failure outcome. Also feeds the per-route
	// failed counter and the latency histogram for Prometheus. routeID is
	// captured via a closure variable that stays "" if no route matched.
	var routeID string
	defer func() {
		status := "Success"
		if !succeeded {
			status = "Failed"
		}
		latency := time.Since(ctx.StartTime)
		s.metrics.RouteOutcome(routeID, succeeded)
		s.metrics.ObserveLatency(latency)
		reqLogger.Info("Request finished",
			zap.Float64("latency_ms", float64(latency.Microseconds())/1000),
			zap.String("status", status),
		)
	}()

	// Attribute masking hits to the per-rule counter so /metrics/prometheus can
	// show "what did the gateway actually strip?" by rule name. Runs after the
	// transform chain has populated ctx.Detections.
	defer func() {
		byRule := make(map[string]int)
		for _, d := range ctx.Detections() {
			byRule[d.Type]++
		}
		for rule, n := range byRule {
			s.metrics.PIIHit(rule, n)
		}
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
	routeID = route.ID
	s.metrics.RouteMatched(route.ID)

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
	provider := providers.NewUniversalProvider(route, reqLogger, s.currentScanner()).WithRetry(s.retry)

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
			// the error is logged and the stream simply ends. A transform rejection
			// is the client's fault and must not count against the breaker.
			reqLogger.Error("Provider stream error", zap.Error(err))
			var terr *providers.TransformError
			if !errors.As(err, &terr) {
				br.RecordFailure()
			} else {
				noteTransformRejection(s, terr)
			}
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
			// A transform rejection is client-fault and skips breaker bookkeeping.
			var terr *providers.TransformError
			if !errors.As(err, &terr) {
				br.RecordFailure()
			} else {
				noteTransformRejection(s, terr)
			}
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
		// A transform rejection (injection block, guard budget) is the client's
		// fault and never reached the upstream: return 400 and leave the breaker
		// untouched so client misuse cannot trip the circuit.
		var terr *providers.TransformError
		if errors.As(err, &terr) {
			noteTransformRejection(s, terr)
			reqLogger.Warn("Request rejected by transform", zap.Error(err))
			http.Error(w, fmt.Sprintf("Request rejected: %v", terr.Err), http.StatusBadRequest)
			return
		}
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

// handleAudit serves the most recent masking-audit records (newest first) from
// the JSONL trail as read-only JSON: GET /admin/audit?limit=N&rule=TYPE. The
// records contain only what the auditor wrote — rule types, counts,
// placeholders and partially-masked previews — never plaintext secrets, so
// this sits on the same no-auth tier as /admin/routes-info (EE builds can
// still gate it via middleware).
func (s *HTTPServer) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	records, err := audit.Query(auditLogPath, audit.QueryOptions{
		Limit:    limit,
		RuleType: r.URL.Query().Get("rule"),
	})
	if err != nil {
		s.logger.Error("Failed to query audit log", zap.Error(err))
		http.Error(w, `{"error":"failed to read audit log"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"enabled": s.auditor.Enabled(),
		"records": records,
	}); err != nil {
		s.logger.Error("Failed to encode audit records", zap.Error(err))
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

// handlePrometheus exposes the same counters as /metrics in the Prometheus
// text exposition format (version 0.0.4), plus per-route counters, a coarse
// latency histogram, per-rule PII hit counters, and injection/transform
// rejection totals — the dimensions that map directly to AIGis's egress
// protection value. Hand-rolled on purpose: the counter set is tiny and stable,
// so pulling in the client library is not worth the dependency. Content is
// metadata only — no request bodies, no secrets.
func (s *HTTPServer) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	snap := s.metrics.Snapshot()
	dims := s.metrics.Dimensions()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	var b strings.Builder
	writeMetric := func(name, help, typ string, value int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", name, help, name, typ, name, value)
	}
	writeMetric("aigis_requests_in_flight", "Requests currently being handled.", "gauge", snap.InFlight)
	writeMetric("aigis_requests_peak_concurrency", "High-water mark of concurrent requests.", "gauge", snap.Peak)
	writeMetric("aigis_requests_total", "Total requests handled.", "counter", snap.Total)
	writeMetric("aigis_requests_success_total", "Requests that completed successfully.", "counter", snap.Success)
	writeMetric("aigis_requests_failed_total", "Requests that failed.", "counter", snap.Failed)
	writeMetric("aigis_uptime_seconds", "Seconds since the gateway started.", "gauge", snap.UptimeSec)

	// Per-route request / failure counters (deterministic ordering).
	fmt.Fprintf(&b, "# HELP aigis_route_requests_total Requests routed to this route.\n# TYPE aigis_route_requests_total counter\n")
	for _, id := range metrics.SortedKeys(dims.RouteRequests) {
		fmt.Fprintf(&b, "aigis_route_requests_total{route=%q} %d\n", id, dims.RouteRequests[id])
	}
	fmt.Fprintf(&b, "# HELP aigis_route_requests_failed_total Requests routed to this route that failed.\n# TYPE aigis_route_requests_failed_total counter\n")
	for _, id := range metrics.SortedKeys(dims.RouteFailed) {
		fmt.Fprintf(&b, "aigis_route_requests_failed_total{route=%q} %d\n", id, dims.RouteFailed[id])
	}

	// Latency histogram (cumulative buckets, Prometheus convention).
	fmt.Fprintf(&b, "# HELP aigis_request_duration_ms_bucket Request latency in milliseconds (cumulative buckets).\n# TYPE aigis_request_duration_ms_bucket counter\n")
	for i, upper := range dims.HistBuckets {
		fmt.Fprintf(&b, "aigis_request_duration_ms_bucket{le=%q} %d\n", strconv.FormatFloat(upper, 'f', -1, 64), dims.HistCounts[i])
	}
	fmt.Fprintf(&b, "aigis_request_duration_ms_bucket{le=\"+Inf\"} %d\n", dims.HistCount)
	fmt.Fprintf(&b, "# HELP aigis_request_duration_ms_sum Sum of request latencies in milliseconds.\n# TYPE aigis_request_duration_ms_sum counter\naigis_request_duration_ms_sum %g\n", dims.HistSumMs)
	fmt.Fprintf(&b, "# HELP aigis_request_duration_ms_count Count of latency observations.\n# TYPE aigis_request_duration_ms_count counter\naigis_request_duration_ms_count %d\n", dims.HistCount)

	// Per-rule PII masking hits — the headline "what did AIGis strip?" view.
	fmt.Fprintf(&b, "# HELP aigis_pii_hits_total Sensitive-info items masked by this rule (e.g. Email, Phone, AWS Access Key).\n# TYPE aigis_pii_hits_total counter\n")
	for _, rule := range metrics.SortedKeys(dims.PIIHits) {
		fmt.Fprintf(&b, "aigis_pii_hits_total{rule=%q} %d\n", rule, dims.PIIHits[rule])
	}

	// Injection / transform-rejection counters (the gateway's "blocked" view).
	writeMetric("aigis_injection_blocked_total", "Requests rejected by the injection transform.", "counter", dims.InjectionBlocked)
	fmt.Fprintf(&b, "# HELP aigis_transform_rejected_total Requests rejected by a request-side transform (injection/guard/pii/...).\n# TYPE aigis_transform_rejected_total counter\n")
	for _, t := range metrics.SortedKeys(dims.TransformRejects) {
		fmt.Fprintf(&b, "aigis_transform_rejected_total{transform=%q} %d\n", t, dims.TransformRejects[t])
	}

	fmt.Fprintf(&b, "# HELP aigis_route_breaker_state Circuit state per route (0=closed, 1=half-open, 2=open).\n# TYPE aigis_route_breaker_state gauge\n")
	for _, route := range s.engine.GetConfig().Routes {
		fmt.Fprintf(&b, "aigis_route_breaker_state{route=%q} %d\n", route.ID, int(s.breakers.Get(route.ID).State()))
	}

	if _, err := io.WriteString(w, b.String()); err != nil {
		s.logger.Error("Failed to write prometheus metrics", zap.Error(err))
	}
}

// generateRequestID generates a simple request ID for tracking
func generateRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}

// noteTransformRejection records a client-side transform rejection in metrics:
// always attributes to the per-transform counter, and additionally bumps the
// dedicated injection counter when the rejection came from an "injection" rule.
func noteTransformRejection(s *HTTPServer, terr *providers.TransformError) {
	if terr.Transform == "" {
		return
	}
	s.metrics.TransformRejected(terr.Transform)
	if terr.Transform == "injection" {
		s.metrics.InjectionBlocked()
	}
}

// sanitizeRequestID validates a caller-supplied X-Request-ID before adopting it:
// max 64 chars of [A-Za-z0-9._-] only, so an attacker cannot inject log fields,
// control characters, or oversized values into logs and the audit trail.
// Anything invalid is discarded (a fresh ID is generated instead).
func sanitizeRequestID(id string) string {
	if id == "" || len(id) > 64 {
		return ""
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return ""
		}
	}
	return id
}
