package core

import "context"

// Provider is the LLM adapter interface
type Provider interface {
	// ID returns the unique identifier for this provider
	ID() string
	// Send sends a raw request body and returns the raw response body
	Send(ctx context.Context, body []byte) ([]byte, error)
	// Stream sends a request and returns a channel for streaming chunks
	Stream(ctx context.Context, body []byte) (<-chan []byte, error)
}
