package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"aigis/internal/config"
	"aigis/internal/core"
	"aigis/internal/core/engine"
	"aigis/internal/core/processors"
	"aigis/internal/core/providers"
	"aigis/internal/pkg/logger"
)

// HTTPServer extends the basic server with gateway functionality
type HTTPServer struct {
	*Server
	pipeline *core.Pipeline
	engine   *engine.Engine
	mux      *http.ServeMux
	logger   *logger.Logger
}

// NewHTTPServer creates a new HTTP server with gateway capabilities
func NewHTTPServer(addr string, zapLogger *zap.Logger) (*HTTPServer, error) {
	baseServer := New(addr)

	// Wrap zap logger with our extension
	extLogger := logger.NewLogger(zapLogger)

	// Initialize pipeline (for logging processor only, transforms are in engine)
	pipeline := core.NewPipeline()

	// Register RequestLogger processor
	pipeline.AddProcessor(processors.NewRequestLogger())

	// Load engine configuration
	engineConfig, err := config.LoadEngineConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load engine config: %w", err)
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

	s := &HTTPServer{
		Server:   baseServer,
		pipeline: pipeline,
		engine:   eng,
		logger:   extLogger,
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

	// Gateway endpoint for LLM requests
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)

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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return s.server.Shutdown(ctx)
}

// handleChatCompletions processes LLM requests through the engine
func (s *HTTPServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	// Only accept POST requests
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Set content type
	w.Header().Set("Content-Type", "application/json")

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

	// Execute the pipeline for request logging
	processedBody, err := s.pipeline.ExecuteRequest(ctx, body)
	if err != nil {
		reqLogger.Error("Pipeline error", zap.Error(err))
		http.Error(w, fmt.Sprintf("Pipeline error: %v", err), http.StatusInternalServerError)
		return
	}

	// Find matching route using engine
	route, err := s.engine.FindRoute(processedBody)
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

	// Create universal provider for this route
	provider := providers.NewUniversalProvider(route, reqLogger)

	// Send request through provider (includes transforms and header handling)
	// Pass the AIGisContext (ctx) instead of r.Context() for bidirectional tokenization
	resp, err := provider.Send(ctx, processedBody, r.Header)
	if err != nil {
		reqLogger.Error("Provider error", zap.Error(err))
		http.Error(w, fmt.Sprintf("Provider error: %v", err), http.StatusBadGateway)
		return
	}

	// Execute the pipeline for response processing (logging)
	finalResp, err := s.pipeline.ExecuteResponse(ctx, resp)
	if err != nil {
		reqLogger.Error("Response pipeline error", zap.Error(err))
		http.Error(w, fmt.Sprintf("Response pipeline error: %v", err), http.StatusInternalServerError)
		return
	}

	// Return the processed response
	w.WriteHeader(http.StatusOK)
	w.Write(finalResp)
}

// generateRequestID generates a simple request ID for tracking
func generateRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}
