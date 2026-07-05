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

	// Iterate through routes in order. The slice header is read under the RLock,
	// so a concurrent Reload cannot mutate it mid-iteration.
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

// GetConfig returns a pointer to the engine's current configuration snapshot.
// Reload replaces this pointer with a new EngineConfig, so a caller-held
// pointer keeps referencing the (immutable) pre-reload snapshot; reload-aware
// consumers must call GetConfig again after a reload rather than caching it.
func (e *Engine) GetConfig() *EngineConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config
}

// Reload atomically swaps the engine's routes and matchers to newCfg. It
// pre-compiles all matchers first; any invalid regex returns an error and
// leaves the live state untouched (fail loud — the gateway keeps running on
// the previous config rather than silently dropping a bad route).
func (e *Engine) Reload(newCfg *EngineConfig) error {
	newMatchers := make(map[string]map[string]*regexp.Regexp, len(newCfg.Routes))
	for _, route := range newCfg.Routes {
		routeMatchers := make(map[string]*regexp.Regexp, len(route.Matcher))
		for jsonPath, pattern := range route.Matcher {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("invalid regex pattern for route %s, path %s: %w", route.ID, jsonPath, err)
			}
			routeMatchers[jsonPath] = re
		}
		newMatchers[route.ID] = routeMatchers
	}

	e.mu.Lock()
	e.config = newCfg
	e.matchers = newMatchers
	e.mu.Unlock()
	return nil
}
