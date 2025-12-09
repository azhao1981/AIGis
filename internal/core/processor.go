package core

// Processor is the middleware interface for the gateway pipeline
type Processor interface {
	// Name returns the processor name
	Name() string
	// Priority returns the execution priority (lower = earlier)
	Priority() int
	// OnRequest is called before the request is sent to the provider
	OnRequest(ctx *AIGisContext, req *ModelRequest) error
	// OnResponse is called after the response is received from the provider
	OnResponse(ctx *AIGisContext, resp interface{}) error
}
