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

	"aigis/internal/config"
	"aigis/internal/core"
	"aigis/internal/core/audit"
	"aigis/internal/core/engine"
	"aigis/internal/core/metrics"
	"aigis/internal/core/providers"
	"aigis/internal/core/transform"
	"aigis/internal/pkg/logger"
)

// HTTPServer extends the basic server with gateway functionality
type HTTPServer struct {
	*Server
	engine  *engine.Engine
	mux     *http.ServeMux
	logger  *logger.Logger
	auditor *audit.Auditor
	metrics *metrics.Metrics
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
			zap.String("upstream", route.Upstream.BaseURL),
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

	s := &HTTPServer{
		Server:  baseServer,
		engine:  eng,
		logger:  extLogger,
		auditor: auditor,
		metrics: metrics.New(),
	}

	// Initialize mux
	s.mux = s.setupRoutes()

	return s, nil
}

// setupRoutes creates and configures the HTTP routes
func (s *HTTPServer) setupRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Concurrency metrics endpoint: current in-flight, peak, and cumulative totals.
	mux.HandleFunc("/metrics", s.handleMetrics)

	// Gateway endpoints for LLM requests.
	// Both share one handler; the engine routes by model regardless of inbound path.
	// /v1/chat/completions: OpenAI-compatible clients
	// /v1/messages: Anthropic-native clients (e.g. Claude Code via ANTHROPIC_BASE_URL)
	mux.HandleFunc("/v1/chat/completions", s.handleGateway)
	mux.HandleFunc("/v1/messages", s.handleGateway)

	// Root endpoint
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":"AIGis is running"}`))
	})

	return mux
}

// Handler returns the HTTP handler for testing
func (s *HTTPServer) Handler() http.Handler {
	return s.mux
}

// Start starts the HTTP server with gateway endpoints
func (s *HTTPServer) Start() error {
	s.server = &http.Server{
		Addr:         s.addr,
		Handler:      s.mux,
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

	// Create a GatewayContext
	ctx := core.NewGatewayContext(r.Context(), reqLogger.Logger)
	ctx.RequestID = requestID
	ctx.TraceID = traceID

	// Audit masked sensitive info at request end (metadata only; no-op if nothing
	// was masked). Covers both streaming and blocking paths via a single defer.
	defer s.auditor.Record(ctx)

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
		zap.String("upstream", route.Upstream.BaseURL),
	)

	// Stash routing metadata for the audit record (read in the deferred Record).
	ctx.SetMetadata("route_id", route.ID)
	ctx.SetMetadata("model", gjson.GetBytes(body, "model").String())

	// Create universal provider for this route
	provider := providers.NewUniversalProvider(route, reqLogger)

	// Branch on streaming: clients set "stream": true to request SSE.
	// The flusher must be available to stream; otherwise fall back to blocking.
	isStream := gjson.GetBytes(body, "stream").Bool()
	if flusher, ok := w.(http.Flusher); isStream && ok {
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
		} else {
			succeeded = true
		}
		return
	}

	// Non-streaming path
	w.Header().Set("Content-Type", "application/json")

	// Send request through provider (includes transforms and header handling)
	// Pass the AIGisContext (ctx) instead of r.Context() for bidirectional tokenization
	resp, err := provider.Send(ctx, body, r.Header)
	if err != nil {
		reqLogger.Error("Provider error", zap.Error(err))
		http.Error(w, fmt.Sprintf("Provider error: %v", err), http.StatusBadGateway)
		return
	}

	// Return the upstream response.
	w.WriteHeader(http.StatusOK)
	w.Write(resp)
	succeeded = true
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
