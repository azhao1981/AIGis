package core

import (
	"context"
	"sync"
)

// AIGisContext extends standard context with gateway-specific fields
type AIGisContext struct {
	context.Context
	RequestID string
	UserID    string
	TraceID   string

	mu       sync.RWMutex
	metadata map[string]interface{}
}

// NewGatewayContext creates a new GatewayContext
func NewGatewayContext(ctx context.Context) *AIGisContext {
	return &AIGisContext{
		Context:  ctx,
		metadata: make(map[string]interface{}),
	}
}

// SetMetadata sets a metadata value (thread-safe)
func (c *AIGisContext) SetMetadata(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metadata[key] = value
}

// GetMetadata gets a metadata value (thread-safe)
func (c *AIGisContext) GetMetadata(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.metadata[key]
	return v, ok
}

// Metadata returns a copy of all metadata (thread-safe)
func (c *AIGisContext) Metadata() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	copy := make(map[string]interface{}, len(c.metadata))
	for k, v := range c.metadata {
		copy[k] = v
	}
	return copy
}
