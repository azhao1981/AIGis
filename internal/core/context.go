package core

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

// AIGisContext extends standard context with gateway-specific fields
type AIGisContext struct {
	context.Context
	RequestID string
	UserID    string
	TraceID   string
	StartTime time.Time
	Log       *zap.Logger

	mu       sync.RWMutex
	metadata map[string]interface{}

	// Vault stores placeholder -> original secret mappings for bidirectional tokenization
	// Map: "__AIGIS_SEC_a1b2c3d4e5f6__" -> "sk-real-key"
	secretVault map[string]string
	vaultMu     sync.RWMutex
}

// NewGatewayContext creates a new GatewayContext
func NewGatewayContext(ctx context.Context, logger *zap.Logger) *AIGisContext {
	return &AIGisContext{
		Context:     ctx,
		StartTime:   time.Now(),
		Log:         logger,
		metadata:    make(map[string]interface{}),
		secretVault: make(map[string]string),
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

// VaultStore stores a placeholder -> original secret mapping (thread-safe)
func (c *AIGisContext) VaultStore(placeholder, original string) {
	c.vaultMu.Lock()
	defer c.vaultMu.Unlock()
	c.secretVault[placeholder] = original
}

// VaultGet retrieves the original secret for a placeholder (thread-safe)
// Returns (original, true) if found, ("", false) otherwise
func (c *AIGisContext) VaultGet(placeholder string) (string, bool) {
	c.vaultMu.RLock()
	defer c.vaultMu.RUnlock()
	original, ok := c.secretVault[placeholder]
	return original, ok
}

// VaultGetAll returns a copy of all vault mappings (thread-safe)
// For debug/logging purposes
func (c *AIGisContext) VaultGetAll() map[string]string {
	c.vaultMu.RLock()
	defer c.vaultMu.RUnlock()
	copy := make(map[string]string, len(c.secretVault))
	for k, v := range c.secretVault {
		copy[k] = v
	}
	return copy
}

