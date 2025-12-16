package engine

import (
	"fmt"
	"regexp"
	"sync"

	"github.com/bytedance/sonic"
)

// Engine is the core transformation engine that handles routing and transformations
type Engine struct {
	config   *EngineConfig
	matchers map[string]map[string]*regexp.Regexp // routeID -> jsonPath -> compiled regex
	mu       sync.RWMutex
}

// NewEngine creates a new transformation engine with the given configuration
func NewEngine(config *EngineConfig) (*Engine, error) {
	e := &Engine{
		config:   config,
		matchers: make(map[string]map[string]*regexp.Regexp),
	}

	// Pre-compile all regex matchers
	for _, route := range config.Routes {
		routeMatchers := make(map[string]*regexp.Regexp)
		for jsonPath, pattern := range route.Matcher {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("invalid regex pattern for route %s, path %s: %w", route.ID, jsonPath, err)
			}
			routeMatchers[jsonPath] = re
		}
		e.matchers[route.ID] = routeMatchers
	}

	return e, nil
}

// FindRoute finds the first matching route for the given request body
func (e *Engine) FindRoute(body []byte) (*Route, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Parse body using sonic
	root, err := sonic.Get(body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	// Iterate through routes in order
	for i := range e.config.Routes {
		route := &e.config.Routes[i]
		routeMatchers := e.matchers[route.ID]

		// Check if all matchers match
		allMatch := true
		for jsonPath, re := range routeMatchers {
			// Get value at JSON path
			node := root.Get(jsonPath)
			if err := node.Check(); err != nil {
				// Path doesn't exist, no match
				allMatch = false
				break
			}

			// Get string value
			value, err := node.String()
			if err != nil {
				// Not a string, try raw value
				rawValue, _ := node.Raw()
				value = rawValue
			}

			// Check if value matches regex
			if !re.MatchString(value) {
				allMatch = false
				break
			}
		}

		if allMatch {
			return route, nil
		}
	}

	return nil, nil // No matching route found
}

// GetConfig returns the engine configuration
func (e *Engine) GetConfig() *EngineConfig {
	return e.config
}
