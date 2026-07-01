package security

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Rule 定义了敏感信息检测规则
type Rule struct {
	Name        string
	Pattern     *regexp.Regexp
	Replacement string
}

// Scanner 扫描并清理文本中的敏感信息
type Scanner struct {
	rules []Rule
}

// NewScanner 创建一个新的 Scanner 实例，内置所有检测规则
func NewScanner() *Scanner {
	scanner := &Scanner{
		rules: make([]Rule, 0),
	}

	// 注册内置规则 - 按照优先级顺序（先匹配更具体的模式）
	// 1. Private Key - 最独特的模式，应该先匹配
	scanner.rules = append(scanner.rules, Rule{
		Name:        "Private Key",
		Pattern:     regexp.MustCompile(`-----BEGIN [A-Z ]+ PRIVATE KEY-----`),
		Replacement: "[PRIVATE_KEY_REDACTED]",
	})

	// 2. AWS Access Key - 非常特定的格式
	scanner.rules = append(scanner.rules, Rule{
		Name:        "AWS Access Key",
		Pattern:     regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		Replacement: "[AWS_AK_REDACTED]",
	})

	// 3. OpenAI API Key - 包括 sk- 和 sk-proj- 格式
	scanner.rules = append(scanner.rules, Rule{
		Name:        "OpenAI API Key",
		Pattern:     regexp.MustCompile(`\bsk-(?:proj-)?[a-zA-Z0-9]{20,}\b`),
		Replacement: "[OPENAI_KEY_REDACTED]",
	})

	// 4. GitHub Token - 特定的前缀和长度
	scanner.rules = append(scanner.rules, Rule{
		Name:        "GitHub Token",
		Pattern:     regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[a-zA-Z0-9]{36}\b`),
		Replacement: "[GITHUB_TOKEN_REDACTED]",
	})

	// 5. Google API Key - 特定的前缀和长度
	scanner.rules = append(scanner.rules, Rule{
		Name:        "Google API Key",
		Pattern:     regexp.MustCompile(`\bAIza[0-9A-Za-z-_]{35}\b`),
		Replacement: "[GOOGLE_KEY_REDACTED]",
	})

	// 6. Email - 更精确的模式，需要在电话之前匹配
	scanner.rules = append(scanner.rules, Rule{
		Name:        "Email",
		Pattern:     regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`),
		Replacement: "[EMAIL_REDACTED]",
	})

	// 7. Mobile Phone - 放在最后
	// 中国手机号：13x, 14x, 15x, 16x, 17x, 18x, 19x 开头，11位
	// 使用 word boundary 避免匹配密钥中的内部数字
	scanner.rules = append(scanner.rules, Rule{
		Name:        "Mobile Phone",
		Pattern:     regexp.MustCompile(`\b(?:\+?86)?\s*(?:1[3-9]\d{9})\b`),
		Replacement: "[PHONE_REDACTED]",
	})

	return scanner
}

// Sanitize 清理文本中的所有敏感信息
// 按顺序应用所有规则，返回清理后的文本
func (s *Scanner) Sanitize(input string) string {
	result := input
	for _, rule := range s.rules {
		result = rule.Pattern.ReplaceAllString(result, rule.Replacement)
	}
	return result
}

// Detect reports the names of rules whose pattern still matches input, without
// modifying it. It is the pre-send leak check for strict-review routes: run it
// on the already-masked, about-to-egress body so any secret a masking rule
// missed is caught before anything leaves the gateway. Empty result = clean.
// extra holds route-scoped rules (mirrors MaskWithExtraRules) so the check
// covers the exact same rule set that masking used.
func (s *Scanner) Detect(input string, extra []Rule) []string {
	var hits []string
	check := func(rules []Rule) {
		for _, rule := range rules {
			if rule.Pattern.MatchString(input) {
				hits = append(hits, rule.Name)
			}
		}
	}
	check(s.rules)
	check(extra)
	return hits
}

// generatePlaceholder generates a unique placeholder for a secret using SHA256 hash
// Format: __AIGIS_SEC_<first 12 chars of SHA256>__
// Using hash ensures the same secret always gets the same placeholder within the request
func generatePlaceholder(original string) string {
	hash := sha256.Sum256([]byte(original))
	hashHex := hex.EncodeToString(hash[:])[:12]
	return fmt.Sprintf("__AIGIS_SEC_%s__", hashHex)
}

// maskPreview returns a partially-masked hint of a secret for audit confirmation:
// the first 2 and last 2 runes are kept, the middle replaced by a fixed "***"
// (the fixed marker avoids leaking the secret's length). Values of length <= 4
// are fully masked. Never returns enough to reconstruct the original.
//
//	"test@example.com" -> "te***om"
//	"13800138000"      -> "13***00"
func maskPreview(s string) string {
	r := []rune(s)
	if len(r) <= 4 {
		return "***"
	}
	return string(r[:2]) + "***" + string(r[len(r)-2:])
}

// Email tokenization modes for MaskOptions.EmailMode.
const (
	EmailModeFull  = "full"  // tokenize the whole address (default)
	EmailModeLocal = "local" // tokenize only the local part, preserve "@domain"
)

// MaskOptions tunes how Mask tokenizes specific rule types. The zero value
// reproduces the original behavior (every match tokenized whole).
type MaskOptions struct {
	// EmailMode selects email tokenization. "" and "full" tokenize the whole
	// address; "local" tokenizes only the local part (before "@") and leaves the
	// domain in place, so the model still sees e.g. a corporate domain without
	// learning the real mailbox. Unmask restores the local part regardless,
	// since the placeholder simply sits in front of the preserved "@domain".
	EmailMode string
}

// Mask replaces sensitive information with placeholders and stores the mapping
// in the vault, using default options. This is for bidirectional tokenization —
// use Unmask() to restore the original values.
func (s *Scanner) Mask(ctx interface{}, input string, tags []string) string {
	return s.MaskWithOptions(ctx, input, tags, MaskOptions{})
}

// MaskWithOptions is Mask with explicit tokenization options (see MaskOptions).
func (s *Scanner) MaskWithOptions(ctx interface{}, input string, tags []string, opts MaskOptions) string {
	return s.MaskWithExtraRules(ctx, input, tags, opts, nil)
}

// MaskWithExtraRules is MaskWithOptions plus per-call extra rules appended after
// the scanner's shared rules. The extra rules are route-scoped (e.g. a PII
// transform's own custom_rules) and never mutate the shared scanner, so it stays
// immutable and concurrency-safe. Extra rules tokenize exactly like built-ins.
func (s *Scanner) MaskWithExtraRules(ctx interface{}, input string, tags []string, opts MaskOptions, extra []Rule) string {
	// ctx should be *core.AIGisContext, but we use interface{} to avoid circular
	// import; we type-assert the vault/audit methods below.

	result := input
	rules := s.rules
	if len(extra) > 0 {
		rules = make([]Rule, 0, len(s.rules)+len(extra))
		rules = append(rules, s.rules...)
		rules = append(rules, extra...)
	}
	for _, rule := range rules {
		// Check if this rule should be applied based on tags
		if len(tags) > 0 {
			shouldApply := false
			for _, tag := range tags {
				if tag == "all" || tag == rule.Name {
					shouldApply = true
					break
				}
			}
			if !shouldApply {
				continue
			}
		}

		// Use ReplaceAllStringFunc to generate unique placeholders for each match
		result = rule.Pattern.ReplaceAllStringFunc(result, func(match string) string {
			// Decide which substring of the match is the secret to tokenize.
			// Default: the whole match. Email-local mode: only the part before
			// "@", keeping "@domain" (suffix) verbatim after the placeholder.
			secret, suffix := match, ""
			if rule.Name == "Email" && opts.EmailMode == EmailModeLocal {
				if at := strings.IndexByte(match, '@'); at > 0 {
					secret, suffix = match[:at], match[at:]
				}
			}

			placeholder := generatePlaceholder(secret)

			// Store the mapping in the vault if ctx is valid
			if ctx != nil {
				// Type assertion to access VaultStore method
				type vaultContext interface {
					VaultStore(placeholder, original string)
				}
				if vaultCtx, ok := ctx.(vaultContext); ok {
					vaultCtx.VaultStore(placeholder, secret)
				}

				// Record an audit entry (rule type + placeholder + masked preview,
				// never full plaintext). Asserted independently of vaultContext so
				// callers/mocks that don't audit are unaffected.
				type auditContext interface {
					RecordDetection(ruleType, placeholder, preview string)
				}
				if auditCtx, ok := ctx.(auditContext); ok {
					auditCtx.RecordDetection(rule.Name, placeholder, maskPreview(secret))
				}
			}

			return placeholder + suffix
		})
	}
	return result
}

// Unmask restores placeholders back to their original secrets from the vault
// It looks for the placeholder pattern: __AIGIS_SEC_[0-9a-f]{12}__
func (s *Scanner) Unmask(ctx interface{}, input string) string {
	if ctx == nil {
		return input
	}

	// Type assertion to access VaultGet method
	type vaultContext interface {
		VaultGet(placeholder string) (string, bool)
	}
	vaultCtx, ok := ctx.(vaultContext)
	if !ok {
		return input
	}

	// Pattern to match our placeholders
	placeholderPattern := regexp.MustCompile(`__AIGIS_SEC_[0-9a-f]{12}__`)

	result := placeholderPattern.ReplaceAllStringFunc(input, func(placeholder string) string {
		if original, found := vaultCtx.VaultGet(placeholder); found {
			return original
		}
		return placeholder // Keep placeholder if not found in vault
	})

	return result
}

// CustomRule is a user-defined detection rule loaded from config
// (security.custom_rules). Name labels the rule (used as the audit rule-type and
// to derive the Sanitize replacement); Pattern is its regular expression. Custom
// rules tokenize exactly like the built-in ones.
type CustomRule struct {
	Name    string `mapstructure:"name"`
	Pattern string `mapstructure:"pattern"`
}

// ruleReplacement derives the bracketed replacement label for a rule name,
// e.g. "Order ID" -> "[ORDER_ID_REDACTED]". Shared by built-in custom rules and
// route-scoped extra rules so both render identically.
func ruleReplacement(name string) string {
	return "[" + strings.ToUpper(strings.ReplaceAll(name, " ", "_")) + "_REDACTED]"
}

// compiledRuleCache memoizes compiled route-scoped rules by "name\x00pattern"
// so per-request CompileRules calls don't re-compile the same regex every time
// (transforms are built per request).
var compiledRuleCache sync.Map // map[string]Rule

// CompileRules turns user-defined CustomRules into compiled Rules, reusing a
// process-wide cache keyed by name+pattern. It returns an error if a rule has an
// empty name or an invalid regexp. Intended for route-scoped rules passed to
// MaskWithExtraRules; it does NOT mutate any shared scanner.
func CompileRules(custom []CustomRule) ([]Rule, error) {
	if len(custom) == 0 {
		return nil, nil
	}
	out := make([]Rule, 0, len(custom))
	for i, r := range custom {
		if r.Name == "" {
			return nil, fmt.Errorf("custom rule #%d has an empty name", i)
		}
		key := r.Name + "\x00" + r.Pattern
		if cached, ok := compiledRuleCache.Load(key); ok {
			out = append(out, cached.(Rule))
			continue
		}
		compiled, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("custom rule %q: %w", r.Name, err)
		}
		rule := Rule{Name: r.Name, Pattern: compiled, Replacement: ruleReplacement(r.Name)}
		compiledRuleCache.Store(key, rule)
		out = append(out, rule)
	}
	return out, nil
}

// NewScannerWithRules builds a scanner with the built-in rules plus the given
// custom rules appended. It returns an error if a custom rule has an empty name
// or an invalid regexp, so a bad config fails loud at startup instead of
// silently dropping a rule. With no custom rules it is equivalent to NewScanner.
func NewScannerWithRules(custom []CustomRule) (*Scanner, error) {
	s := NewScanner()
	for i, r := range custom {
		if r.Name == "" {
			return nil, fmt.Errorf("custom rule #%d has an empty name", i)
		}
		if err := s.AddRule(r.Name, r.Pattern, ruleReplacement(r.Name)); err != nil {
			return nil, fmt.Errorf("custom rule %q: %w", r.Name, err)
		}
	}
	return s, nil
}

// AddRule 动态添加自定义规则
func (s *Scanner) AddRule(name string, pattern string, replacement string) error {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	s.rules = append(s.rules, Rule{
		Name:        name,
		Pattern:     compiled,
		Replacement: replacement,
	})
	return nil
}

// GetRules 返回当前所有规则的副本（仅供查看，不可修改）
func (s *Scanner) GetRules() []Rule {
	rulesCopy := make([]Rule, len(s.rules))
	copy(rulesCopy, s.rules)
	return rulesCopy
}
