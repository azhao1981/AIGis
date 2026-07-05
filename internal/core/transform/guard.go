package transform

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"aigis/internal/core"
)

// GuardTransform enforces inbound request budgets before the request is sent
// upstream, so an oversized body or an over-budget max_tokens is rejected here
// rather than paying the upstream for it. It is a request-side transform (plugs
// into the existing pipeline, no core changes) and never mutates the body.
type GuardTransform struct{}

func (t *GuardTransform) Name() string { return TypeGuard }

func (t *GuardTransform) Apply(_ *core.AIGisContext, body []byte, config map[string]string) ([]byte, error) {
	if v := strings.TrimSpace(config["max_bytes"]); v != "" {
		limit, err := strconv.Atoi(v)
		if err != nil || limit < 0 {
			return nil, fmt.Errorf("guard: invalid max_bytes %q", v)
		}
		if limit > 0 && len(body) > limit {
			return nil, fmt.Errorf("guard: request body %d bytes exceeds max_bytes %d", len(body), limit)
		}
	}

	if v := strings.TrimSpace(config["max_tokens"]); v != "" {
		limit, err := strconv.Atoi(v)
		if err != nil || limit < 0 {
			return nil, fmt.Errorf("guard: invalid max_tokens %q", v)
		}
		if limit > 0 {
			if mt := gjson.GetBytes(body, "max_tokens"); mt.Exists() && mt.Int() > int64(limit) {
				return nil, fmt.Errorf("guard: requested max_tokens %d exceeds limit %d", mt.Int(), limit)
			}
		}
	}

	return body, nil
}
