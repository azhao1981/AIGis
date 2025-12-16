package engine

// EngineConfig defines the configuration for the transformation engine
type EngineConfig struct {
	Routes []Route `mapstructure:"routes"`
}

// Route defines a routing rule with matcher, upstream, and transformations
type Route struct {
	// ID is the unique identifier for this route
	ID string `mapstructure:"id"`
	// Matcher maps JSON path (e.g., "model") to regex pattern (e.g., "^gpt-.*")
	Matcher map[string]string `mapstructure:"matcher"`
	// Upstream defines the target backend service
	Upstream Upstream `mapstructure:"upstream"`
	// Transforms is the pipeline of transformations to apply
	Transforms []TransformStep `mapstructure:"transforms"`
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
	// HeaderName is the header name for "header" auth strategy (default: "Authorization")
	HeaderName string `mapstructure:"header_name"`
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

// TransformType constants
const (
	TransformTypePII      = "pii"       // PII redaction
	TransformTypeFieldMap = "field_map" // Field mapping using gjson/sjson
	TransformTypeTemplate = "template"  // Go text/template transformation
)
