package engine

import (
	"fmt"
	"regexp"
)

// validAuthStrategies is the set of auth_strategy values the provider understands.
// "none" and "" both mean "no built-in auth" (auth is handled via header_policy,
// e.g. the claude-proxy route that sets x-api-key, or there is no auth at all).
var validAuthStrategies = map[string]bool{
	AuthStrategyBearer: true,
	AuthStrategyHeader: true,
	AuthStrategyQuery:  true,
	"none":             true,
	"":                 true,
}

// Validate checks the engine configuration for structural errors that would
// otherwise surface only at request time — or worse, silently misroute. It is
// meant to run once at startup so a bad config fails loud and early instead of
// during a live request.
//
// knownTransforms and knownStreamTranslators are the sets of names the runtime
// can dispatch. They are passed in (rather than imported) to keep this package
// decoupled from the transform implementations — see transform.KnownTypes() and
// transform.KnownStreamTranslators().
//
// Rules enforced:
//   - at least one route is configured
//   - every route ID is non-empty and unique
//   - every matcher pattern is a valid regexp
//   - every route has an upstream base_url
//   - every auth_strategy is recognized
//   - every transform type is known
//   - every stream_translate name is known
//   - at least one catch-all route (empty matcher) exists, else some requests 404
func (c *EngineConfig) Validate(knownTransforms, knownStreamTranslators map[string]bool) error {
	if len(c.Routes) == 0 {
		return fmt.Errorf("engine config has no routes")
	}

	seenIDs := make(map[string]bool, len(c.Routes))
	hasCatchAll := false

	for i := range c.Routes {
		route := &c.Routes[i]

		if route.ID == "" {
			return fmt.Errorf("route #%d has an empty id", i)
		}
		if seenIDs[route.ID] {
			return fmt.Errorf("duplicate route id %q", route.ID)
		}
		seenIDs[route.ID] = true

		for jsonPath, pattern := range route.Matcher {
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("route %q: invalid regex for matcher %q: %w", route.ID, jsonPath, err)
			}
		}
		if len(route.Matcher) == 0 {
			hasCatchAll = true
		}

		if route.Upstream.BaseURL == "" {
			return fmt.Errorf("route %q: upstream base_url is empty", route.ID)
		}

		if !validAuthStrategies[route.Upstream.AuthStrategy] {
			return fmt.Errorf("route %q: unknown auth_strategy %q", route.ID, route.Upstream.AuthStrategy)
		}

		for _, step := range route.Transforms {
			if !knownTransforms[step.Type] {
				return fmt.Errorf("route %q: unknown transform type %q", route.ID, step.Type)
			}
		}
		for _, step := range route.ResponseTransforms {
			if !knownTransforms[step.Type] {
				return fmt.Errorf("route %q: unknown response transform type %q", route.ID, step.Type)
			}
		}

		if !knownStreamTranslators[route.StreamTranslate] {
			return fmt.Errorf("route %q: unknown stream_translate %q", route.ID, route.StreamTranslate)
		}
	}

	if !hasCatchAll {
		return fmt.Errorf("no catch-all route (one with an empty matcher) configured; unmatched requests would 404")
	}

	return nil
}
