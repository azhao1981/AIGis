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
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"aigis/internal/core"
	"aigis/internal/core/processors"
	"aigis/internal/core/providers"
	"aigis/internal/pkg/logger"
)

// HTTPServer extends the basic server with gateway functionality
type HTTPServer struct {
	*Server
	pipeline *core.Pipeline
	provider core.Provider
	mux      *http.ServeMux
	logger   *logger.Logger
}

// NewHTTPServer creates a new HTTP server with gateway capabilities
func NewHTTPServer(addr string, zapLogger *zap.Logger) *HTTPServer {
	baseServer := New(addr)

	// Wrap zap logger with our extension
	extLogger := logger.NewLogger(zapLogger)

	// Initialize pipeline
	pipeline := core.NewPipeline()

	// Register RequestLogger processor first
	pipeline.AddProcessor(processors.NewRequestLogger())

	// Register PII Guard processor
	pipeline.AddProcessor(processors.NewPIIGuard())

	// Initialize OpenAI provider from config
	apiKey := viper.GetString("openai.api_key")
	baseURL := viper.GetString("openai.base_url")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	provider := providers.NewOpenAIProvider(apiKey, baseURL)

	s := &HTTPServer{
		Server:   baseServer,
		pipeline: pipeline,
		provider: provider,
		logger:   extLogger,
	}

	// Initialize mux
	s.mux = s.setupRoutes()

	return s
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
		// Skip 0 层，显示这个 goroutine 的实际调用位置（Start 方法）
		s.logger.Info("Starting AIGis", zap.String("addr", s.addr))
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Fatal("Server error", zap.Error(err))
		}
	}()

	<-stop
	// Skip 0 层，因为包装器会自动 Skip 1
	s.logger.Skip(0).Info("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return s.server.Shutdown(ctx)
}

// handleChatCompletions processes LLM requests through the pipeline
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
		// 不需要额外 Skip，因为包装器已经 Skip 了 1 层
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

	// Create a GatewayContext - 需要获取底层的 zap.Logger
	ctx := core.NewGatewayContext(r.Context(), reqLogger.Logger)
	ctx.RequestID = requestID
	ctx.TraceID = traceID

	// Execute the pipeline for request processing (PII redaction)
	processedBody, err := s.pipeline.ExecuteRequest(ctx, body)
	if err != nil {
		// 不需要额外 Skip，因为包装器已经 Skip 了 1 层
		reqLogger.Error("Pipeline error", zap.Error(err))
		http.Error(w, fmt.Sprintf("Pipeline error: %v", err), http.StatusInternalServerError)
		return
	}

	// Forward the processed request to OpenAI
	resp, err := s.provider.Send(r.Context(), processedBody)
	if err != nil {
		// 不需要额外 Skip，因为包装器已经 Skip 了 1 层
		reqLogger.Error("Provider error", zap.Error(err))
		http.Error(w, fmt.Sprintf("Provider error: %v", err), http.StatusBadGateway)
		return
	}

	// Execute the pipeline for response processing
	finalResp, err := s.pipeline.ExecuteResponse(ctx, resp)
	if err != nil {
		// 不需要额外 Skip，因为包装器已经 Skip 了 1 层
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
