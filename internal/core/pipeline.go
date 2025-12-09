package core

import (
	"sort"
)

// Pipeline holds a collection of processors and manages their execution
type Pipeline struct {
	processors []Processor
}

// NewPipeline creates a new pipeline instance
func NewPipeline() *Pipeline {
	return &Pipeline{
		processors: make([]Processor, 0),
	}
}

// AddProcessor adds a processor to the pipeline
func (p *Pipeline) AddProcessor(processor Processor) {
	p.processors = append(p.processors, processor)
}

// ExecuteRequest executes all processors' OnRequest methods in priority order
func (p *Pipeline) ExecuteRequest(ctx *AIGisContext, req *ModelRequest) error {
	// Create a copy of processors to avoid modifying the original slice
	sortedProcessors := make([]Processor, len(p.processors))
	copy(sortedProcessors, p.processors)

	// Sort processors by priority (lower number = higher priority = runs earlier)
	sort.Slice(sortedProcessors, func(i, j int) bool {
		return sortedProcessors[i].Priority() < sortedProcessors[j].Priority()
	})

	// Execute each processor's OnRequest method
	for _, processor := range sortedProcessors {
		if err := processor.OnRequest(ctx, req); err != nil {
			return err
		}
	}

	return nil
}

// ExecuteResponse executes all processors' OnResponse methods in priority order
func (p *Pipeline) ExecuteResponse(ctx *AIGisContext, resp interface{}) error {
	// Create a copy of processors to avoid modifying the original slice
	sortedProcessors := make([]Processor, len(p.processors))
	copy(sortedProcessors, p.processors)

	// Sort processors by priority (lower number = higher priority = runs earlier)
	sort.Slice(sortedProcessors, func(i, j int) bool {
		return sortedProcessors[i].Priority() < sortedProcessors[j].Priority()
	})

	// Execute each processor's OnResponse method
	for _, processor := range sortedProcessors {
		if err := processor.OnResponse(ctx, resp); err != nil {
			return err
		}
	}

	return nil
}