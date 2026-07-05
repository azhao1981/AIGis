package transform

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/tidwall/gjson"

	"aigis/internal/core"
)

// injectionMode selects what happens on a detected prompt-injection pattern.
const (
	injectionModeBlock = "block" // default: hit -> error (mapped to 400 upstream)
	injectionModeWarn  = "warn"  // hit -> mark ctx metadata, let the request through
)

// builtinInjectionPatterns are case-insensitive heuristics for the most common
// prompt-injection / jailbreak attempts against an LLM. They are deliberately
// conservative (well-known attack phrasings) to keep false positives low; a
// route can add its own via the "extra_patterns" config.
var builtinInjectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?(the\s+)?(previous|above|prior|earlier)\s+(instructions|prompts?|rules|messages)`),
	regexp.MustCompile(`(?i)disregard\s+(all\s+)?(the\s+)?(previous|above|prior)?\s*(instructions|prompts?|rules)`),
	regexp.MustCompile(`(?i)forget\s+(everything|all)\s+(you|that|above|previous)`),
	regexp.MustCompile(`(?i)you\s+are\s+now\s+(a|an|in)\b`),
	regexp.MustCompile(`(?i)\bDAN\b.{0,20}(mode|jailbreak|do\s+anything)`),
	regexp.MustCompile(`(?i)do\s+anything\s+now`),
	regexp.MustCompile(`(?i)developer\s+mode`),
	regexp.MustCompile(`(?i)(reveal|print|repeat|show|leak)\s+(me\s+)?(your|the)\s+(system\s+prompt|initial\s+instructions|instructions)`),
	regexp.MustCompile(`(?i)(pretend|act)\s+(you\s+are|as\s+if|to\s+be)\b.{0,40}(no\s+restrictions|unfiltered|without\s+(any\s+)?(rules|restrictions|guidelines))`),
	regexp.MustCompile(`(?i)bypass\s+(your|the|all)\s+(restrictions|guidelines|safety|filters?)`),
}

// InjectionTransform scans request text for prompt-injection / jailbreak
// patterns. It is a request-side transform so it plugs into the existing
// pipeline with no core changes. It never mutates the body: in block mode a hit
// aborts the request; in warn mode a hit is recorded in ctx metadata.
type InjectionTransform struct{}

func (t *InjectionTransform) Name() string { return TypeInjection }

func (t *InjectionTransform) Apply(ctx *core.AIGisContext, body []byte, config map[string]string) ([]byte, error) {
	mode := config["mode"]
	if mode == "" {
		mode = injectionModeBlock
	}

	extra, err := parseExtraPatterns(config["extra_patterns"])
	if err != nil {
		return nil, err
	}

	text := extractPromptText(body)
	hits := matchInjection(text, extra)
	if len(hits) == 0 {
		return body, nil
	}

	joined := strings.Join(hits, ", ")
	if mode == injectionModeWarn {
		ctx.SetMetadata("injection_hits", joined)
		return body, nil
	}
	return nil, fmt.Errorf("prompt injection detected: %s", joined)
}

// matchInjection returns the (0-based) indices of built-in patterns and the
// names of extra patterns that matched, as human-readable labels.
func matchInjection(text string, extra []namedPattern) []string {
	var hits []string
	for i, re := range builtinInjectionPatterns {
		if re.MatchString(text) {
			hits = append(hits, fmt.Sprintf("builtin#%d", i))
		}
	}
	for _, p := range extra {
		if p.re.MatchString(text) {
			hits = append(hits, p.name)
		}
	}
	return hits
}

// namedPattern is a route-scoped extra injection rule.
type namedPattern struct {
	name string
	re   *regexp.Regexp
}

// extraPatternDef is the JSON shape of one "extra_patterns" entry.
type extraPatternDef struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
}

// parseExtraPatterns compiles the optional "extra_patterns" config (a JSON array
// of {name, pattern}) into route-scoped rules. Empty yields none; invalid JSON
// or a bad regex returns an error so a misconfigured route fails loud.
func parseExtraPatterns(s string) ([]namedPattern, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var defs []extraPatternDef
	if err := sonic.Unmarshal([]byte(s), &defs); err != nil {
		return nil, fmt.Errorf("invalid extra_patterns JSON: %w", err)
	}
	out := make([]namedPattern, 0, len(defs))
	for i, d := range defs {
		if strings.TrimSpace(d.Name) == "" {
			return nil, fmt.Errorf("extra_patterns #%d has an empty name", i)
		}
		re, err := regexp.Compile(d.Pattern)
		if err != nil {
			return nil, fmt.Errorf("extra_patterns %q: %w", d.Name, err)
		}
		out = append(out, namedPattern{name: d.Name, re: re})
	}
	return out, nil
}

// extractPromptText concatenates the user-controlled text of a request body so a
// single scan covers it. It tolerates both the OpenAI shape (messages[].content
// as string) and the Claude shape (top-level "system" plus content that may be a
// string or an array of {type:"text", text:...} blocks). A parse failure yields
// the raw body as a fallback so detection still runs.
func extractPromptText(body []byte) string {
	if !gjson.ValidBytes(body) {
		return string(body)
	}
	var b strings.Builder
	root := gjson.ParseBytes(body)

	if sys := root.Get("system"); sys.Type == gjson.String {
		b.WriteString(sys.String())
		b.WriteByte('\n')
	}

	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		switch content.Type {
		case gjson.String:
			b.WriteString(content.String())
			b.WriteByte('\n')
		default:
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "text" {
					b.WriteString(block.Get("text").String())
					b.WriteByte('\n')
				}
				return true
			})
		}
		return true
	})

	if b.Len() == 0 {
		return string(body)
	}
	return b.String()
}
