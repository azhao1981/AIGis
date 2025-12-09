package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"aigis/internal/core"
	"aigis/internal/core/processors"
)

// HTTPServer extends the basic server with gateway functionality
type HTTPServer struct {
	*Server
	pipeline *core.Pipeline
}

// NewHTTPServer creates a new HTTP server with gateway capabilities
func NewHTTPServer(addr string) *HTTPServer {
	baseServer := New(addr)

	// Initialize pipeline
	pipeline := core.NewPipeline()

	// Register PII Guard processor
	pipeline.AddProcessor(processors.NewPIIGuard())

	return &HTTPServer{
		Server:   baseServer,
		pipeline: pipeline,
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
	// Only accept POST requests
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Set content type
	w.Header().Set("Content-Type", "application/json")

	// Parse the incoming JSON into ModelRequest
	var req core.ModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// Create a GatewayContext
	ctx := core.NewGatewayContext(r.Context())
	ctx.RequestID = generateRequestID()

	// Execute the pipeline for request processing
	if err := s.pipeline.ExecuteRequest(ctx, &req); err != nil {
		http.Error(w, fmt.Sprintf("Pipeline error: %v", err), http.StatusInternalServerError)
		return
	}

	// For MVP: return the modified request as JSON to verify redaction
	// In a real implementation, this would forward to an LLM provider
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(req)
}

// generateRequestID generates a simple request ID for tracking
func generateRequestID() string {
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}