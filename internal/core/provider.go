package core

import "context"

// ModelRequest represents a request to an LLM provider
type ModelRequest struct {
	Model    string        `json:"model"`
	Messages []interface{} `json:"messages"`
	Stream   bool          `json:"stream"`
}

// Provider is the LLM adapter interface
type Provider interface {
	// ID returns the unique identifier for this provider
	ID() string
	// Send sends a request and returns the response
	Send(ctx context.Context, req *ModelRequest) (interface{}, error)
	// Stream sends a request and returns a channel for streaming chunks
	Stream(ctx context.Context, req *ModelRequest) (<-chan string, error)
}
