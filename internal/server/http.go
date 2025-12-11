package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/viper"

	"aigis/internal/core"
	"aigis/internal/core/processors"
	"aigis/internal/core/providers"
)

// HTTPServer extends the basic server with gateway functionality
type HTTPServer struct {
	*Server
	pipeline *core.Pipeline
	provider core.Provider
}

// NewHTTPServer creates a new HTTP server with gateway capabilities
func NewHTTPServer(addr string) *HTTPServer {
	baseServer := New(addr)

	// Initialize pipeline
	pipeline := core.NewPipeline()

	// Register PII Guard processor
	pipeline.AddProcessor(processors.NewPIIGuard())

	// Initialize OpenAI provider from config
	apiKey := viper.GetString("openai.api_key")
	baseURL := viper.GetString("openai.base_url")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	provider := providers.NewOpenAIProvider(apiKey, baseURL)

	return &HTTPServer{
		Server:   baseServer,
		pipeline: pipeline,
		provider: provider,
	}
}

// Start starts the HTTP server with gateway endpoints
func (s *HTTPServer) Start() error {
	mux := http.NewServeMux()

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Gateway endpoint for LLM requests
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		s.handleChatCompletions(w, r)
	})

	// Root endpoint
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":"AIGis is running"}`))
	})

	s.server = &http.Server{
		Addr:         s.addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Starting AIGis on %s", s.addr)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-stop
	fmt.Println("\nShutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return s.server.Shutdown(ctx)
}

// handleChatCompletions processes LLM requests through the pipeline
func (s *HTTPServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	log.Printf("[Gateway] Received request: %s %s", r.Method, r.URL.Path)

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
		http.Error(w, fmt.Sprintf("Failed to read body: %v", err), http.StatusBadRequest)
		return
	}

	log.Printf("[Gateway] Received body length: %d", len(body))

	// Create a GatewayContext
	ctx := core.NewGatewayContext(r.Context())
	ctx.RequestID = generateRequestID()

	// Execute the pipeline for request processing (PII redaction)
	processedBody, err := s.pipeline.ExecuteRequest(ctx, body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Pipeline error: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("[Gateway] Pipeline executed, calling provider")

	// Forward the processed request to OpenAI
	resp, err := s.provider.Send(r.Context(), processedBody)
	if err != nil {
		log.Printf("[Gateway] Provider error: %v", err)
		http.Error(w, fmt.Sprintf("Provider error: %v", err), http.StatusBadGateway)
		return
	}

	log.Printf("[Gateway] Provider returned, response length: %d", len(resp))

	// Return the raw response from OpenAI
	w.WriteHeader(http.StatusOK)
	w.Write(resp)
}

// generateRequestID generates a simple request ID for tracking
func generateRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}
