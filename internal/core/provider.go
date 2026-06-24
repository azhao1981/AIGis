package core

import (
	"io"
	"net/http"
)

// Provider is the LLM adapter interface
type Provider interface {
	// ID returns the unique identifier for this provider
	ID() string
	// Send sends a raw request body with original headers and returns the raw response body
	Send(ctx *AIGisContext, body []byte, originalHeaders http.Header) ([]byte, error)
	// SendStream streams the upstream SSE response through to w, flushing after each chunk
	SendStream(ctx *AIGisContext, body []byte, originalHeaders http.Header, w io.Writer, flusher http.Flusher) error
}
