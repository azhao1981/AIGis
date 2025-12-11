package core

// Processor is the middleware interface for the gateway pipeline
type Processor interface {
	// Name returns the processor name
	Name() string
	// Priority returns the execution priority (lower = earlier)
	Priority() int
	// OnRequest receives the raw body, modifies it (if needed), and returns the new body.
	// If no change is needed, it can return the original body.
	OnRequest(ctx *AIGisContext, body []byte) ([]byte, error)
	// OnResponse handles the raw response body.
	OnResponse(ctx *AIGisContext, body []byte) ([]byte, error)
}
