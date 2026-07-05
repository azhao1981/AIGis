package engine

import (
	"os"
	"strings"
)

// EngineConfig defines the configuration for the transformation engine
type EngineConfig struct {
	Routes []Route `mapstructure:"routes"`
}

// Route defines a routing rule with matcher, upstream, and transformations
type Route struct {
	// ID is the unique identifier for this route
	ID string `mapstructure:"id"`
	// Matcher maps JSON path (e.g., "model") to regex pattern (e.g., "gpt-.*")
	Matcher map[string]string `mapstructure:"matcher"`
	// Upstream defines the target backend service
	Upstream Upstream `mapstructure:"upstream"`
	// Transforms is the pipeline of transformations applied to the REQUEST body.
	Transforms []TransformStep `mapstructure:"transforms"`
	// ResponseTransforms is the pipeline applied to the (non-streaming) RESPONSE
	// body before unmask — e.g. reshaping a Dify response back to OpenAI form.
	ResponseTransforms []TransformStep `mapstructure:"response_transforms"`
	// StreamTranslate names the stream response translator for the SSE path
	// (e.g. "dify" to translate Dify SSE into OpenAI chunks). Empty/"unmask"
	// means passthrough + cross-chunk unmask. Validated against known names.
	StreamTranslate string `mapstructure:"stream_translate"`
	// ForceBlock enables strict egress review on this route: a streaming request
	// (stream:true) is served internally via the blocking path so a final
	// pre-send leak check can run on the fully-masked body before anything is
	// sent upstream. The client still gets an SSE response (the buffered result
	// is re-emitted as a pseudo-stream), so it is unaware of the downgrade.
	ForceBlock bool `mapstructure:"force_block"`
	// HeaderPolicy defines how to handle HTTP headers
	HeaderPolicy HeaderPolicy `mapstructure:"header_policy"`
}

// HeaderPolicy defines rules for handling HTTP headers
type HeaderPolicy struct {
	// Allow lists headers to pass through from client requests
	Allow []string `mapstructure:"allow"`
	// Set maps headers to force set (supports "env:VAR" syntax for env vars)
	Set map[string]string `mapstructure:"set"`
	// Remove lists headers to exclude from upstream requests
	Remove []string `mapstructure:"remove"`
}

// Upstream defines the backend service configuration
type Upstream struct {
	// BaseURL is the base URL for the upstream service (e.g., "https://api.openai.com/v1")
	BaseURL string `mapstructure:"base_url"`
	// Path is the endpoint path (e.g., "/chat/completions")
	Path string `mapstructure:"path"`
	// AuthStrategy defines how to authenticate: "bearer", "header", "query"
	AuthStrategy string `mapstructure:"auth_strategy"`
	// TokenEnv is the environment variable name to read the token from
	TokenEnv string `mapstructure:"token_env"`
	// TokenEnvs is an optional list of env var names for multi-key load
	// balancing: the provider round-robins across the keys that resolve to a
	// non-empty value. When empty, TokenEnv (single key) is used; when set, it
	// takes precedence over TokenEnv.
	TokenEnvs []string `mapstructure:"token_envs"`
	// HeaderName is the header name for "header" auth strategy (default: "Authorization")
	HeaderName string `mapstructure:"header_name"`
}

// ResolvedBaseURL returns BaseURL with the "env:VAR" syntax resolved to the
// environment variable's value. A plain URL is returned unchanged. This is the
// single source of truth for both request building and logging, so logs show
// the real upstream rather than the raw "env:..." config value.
func (u Upstream) ResolvedBaseURL() string {
	if envVar, ok := strings.CutPrefix(u.BaseURL, "env:"); ok {
		return os.Getenv(envVar)
	}
	return u.BaseURL
}

// TransformStep defines a single transformation in the pipeline
type TransformStep struct {
	// Type is the transformation type: "pii", "field_map", "template"
	Type string `mapstructure:"type"`
	// Config contains type-specific configuration
	Config map[string]string `mapstructure:"config"`
}

// AuthStrategy constants
const (
	AuthStrategyBearer = "bearer" // Authorization: Bearer <token>
	AuthStrategyHeader = "header" // Custom header with token value
	AuthStrategyQuery  = "query"  // Query parameter with token value
)

// TransformType constants are owned by the transform package alongside their
// implementations (internal/core/transform).
