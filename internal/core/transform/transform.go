// Package transform provides pluggable request/response transformation strategies.
//
// Each Transformer is a self-contained algorithm with a pure data contract
// (bytes in, bytes out). Adding a new transformation means writing a new
// Transformer and registering it — no existing code changes (Open/Closed).
package transform

import (
	"aigis/internal/core"
	"aigis/internal/core/security"
)

// Transform type names. These are the values used in route config
// (config.yaml: transforms[].type) and are owned here alongside their
// implementations rather than in the engine config package.
const (
	TypePII       = "pii"        // PII redaction (OpenAI format)
	TypePIIClaude = "pii_claude" // PII redaction (Claude/Anthropic format)
	TypeFieldMap  = "field_map"  // Field mapping using gjson/sjson
	TypeTemplate  = "template"   // Go text/template transformation
	TypeUnmask    = "unmask"     // Restore tokenized secrets in a response body
	TypeInjection = "injection"  // Prompt-injection / jailbreak detection
	TypeGuard     = "guard"      // Inbound size / token budget pre-check
)

// Transformer is the algorithm contract for a single transformation step.
// It takes a request/response body and returns the transformed body.
// Implementations must stay pure: they receive only the body, the context
// (for the secret vault), and step config — never Provider or HTTP details.
type Transformer interface {
	// Name is the type identifier matched against route config (e.g. "pii").
	Name() string
	// Apply transforms body using the optional step config.
	Apply(ctx *core.AIGisContext, body []byte, config map[string]string) ([]byte, error)
}

// Registry dispatches transform steps to their Transformer by name.
type Registry struct {
	transformers map[string]Transformer
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{transformers: make(map[string]Transformer)}
}

// NewDefaultRegistry builds a registry with all built-in transformers wired
// to the given scanner (shared so the secret vault is consistent).
func NewDefaultRegistry(scanner *security.Scanner) *Registry {
	r := NewRegistry()
	r.Register(&PIITransform{name: TypePII, format: formatOpenAI, scanner: scanner})
	r.Register(&PIITransform{name: TypePIIClaude, format: formatClaude, scanner: scanner})
	r.Register(&FieldMapTransform{})
	r.Register(&TemplateTransform{})
	r.Register(&UnmaskTransform{scanner: scanner})
	r.Register(&InjectionTransform{})
	r.Register(&GuardTransform{})
	return r
}

// KnownTypes returns the set of transform type names the default registry
// supports. It exists so config validation can reject unknown transform types
// at startup without the engine package depending on these implementations.
func KnownTypes() map[string]bool {
	return map[string]bool{
		TypePII:       true,
		TypePIIClaude: true,
		TypeFieldMap:  true,
		TypeTemplate:  true,
		TypeUnmask:    true,
		TypeInjection: true,
		TypeGuard:     true,
	}
}

// Register adds a transformer, keyed by its Name().
func (r *Registry) Register(t Transformer) {
	r.transformers[t.Name()] = t
}

// Get returns the transformer for a type name, and whether it was found.
func (r *Registry) Get(name string) (Transformer, bool) {
	t, ok := r.transformers[name]
	return t, ok
}
