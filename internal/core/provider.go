package core

import (
	"context"
	"net/http"
)

// Provider is the LLM adapter interface
type Provider interface {
	// ID returns the unique identifier for this provider
	ID() string
	// Send sends a raw request body with original headers and returns the raw response body
	Send(ctx *AIGisContext, body []byte, originalHeaders http.Header) ([]byte, error)
	// Stream sends a request and returns a channel for streaming chunks
	Stream(ctx context.Context, body []byte, originalHeaders http.Header) (<-chan []byte, error)
}
