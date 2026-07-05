package security

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Rule 定义了敏感信息检测规则
// Validator, when non-nil, runs on each regex match: only matches it accepts
// are treated as sensitive. This lets checksum-bearing identifiers (bank cards
// via Luhn, China ID via GB11643) reject look-alike digit runs, cutting false
// positives. A nil Validator (all existing/custom rules) keeps prior behavior.
type Rule struct {
	Name        string
	Pattern     *regexp.Regexp
	Replacement string
	Validator   func(match string) bool
}

// valid reports whether a regex match passes the rule's validator (vacuously
// true when the rule has none).
func (r Rule) valid(match string) bool {
	return r.Validator == nil || r.Validator(match)
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
	// 1. Private Key - 最独特的模式，应该先匹配。
	// 匹配 BEGIN..END 整块（含 base64 密钥体），而非只撕掉头部标签；
	// END 缺失（截断粘贴）时可选组退化为只匹配头部，保底不放行标记行。
	// (?:[A-Z]+ )* 覆盖 "PRIVATE KEY" / "RSA PRIVATE KEY" / "ENCRYPTED PRIVATE KEY" 等变体。
	scanner.rules = append(scanner.rules, Rule{
		Name:        "Private Key",
		Pattern:     regexp.MustCompile(`-----BEGIN (?:[A-Z]+ )*PRIVATE KEY-----(?:[\s\S]*?-----END (?:[A-Z]+ )*PRIVATE KEY-----)?`),
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

	// 5b. Anthropic API Key - sk-ant-* 多段式（含 api03/admin-api/test 等可选
	// 子前缀）。前缀段长度上限 12（覆盖 "admin-api03" 这种），尾段 base62 >= 90。
	// 放在 OpenAI 规则之后，前缀不重叠。
	scanner.rules = append(scanner.rules, Rule{
		Name:        "Anthropic API Key",
		Pattern:     regexp.MustCompile(`\bsk-ant-[a-z0-9-]{1,12}-[A-Za-z0-9_-]{90,}\b`),
		Replacement: "[ANTHROPIC_KEY_REDACTED]",
	})

	// 5c. Slack token 家族 - xox[bpoas]- 前缀 + 10-72 位 base62，覆盖
	// bot/user/legacy/app/refresh token 五种。
	scanner.rules = append(scanner.rules, Rule{
		Name:        "Slack Token",
		Pattern:     regexp.MustCompile(`\bxox[bpoas]-[A-Za-z0-9-]{10,72}\b`),
		Replacement: "[SLACK_TOKEN_REDACTED]",
	})

	// 5d. Stripe live secret key - sk_live_ + 24+ 位 alphanum，足够独特。
	scanner.rules = append(scanner.rules, Rule{
		Name:        "Stripe Key",
		Pattern:     regexp.MustCompile(`\bsk_live_[A-Za-z0-9]{24,}\b`),
		Replacement: "[STRIPE_KEY_REDACTED]",
	})

	// 5e. JWT - 三段 eyJ..eyJ..sig（base64url header.payload），头两段都以
	// eyJ 开头（base64 编码的 `{"`），第三段任意 base64url；总长 >= 30 过滤短串。
	// 校验位用 Validator：尝试解码头两段为 JSON，失败则视为 look-alike 放行。
	scanner.rules = append(scanner.rules, Rule{
		Name:        "JWT",
		Pattern:     regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
		Replacement: "[JWT_REDACTED]",
		Validator:   validJWT,
	})

	// 5f. 赋值型泄露 - "password=secret"/"api_key: sk-.."/"secret: ..." 等常见
	// 键值语法。键名白名单（password/passwd/secret/api_key/apikey/access_key/
	// private_key/token）+ 分隔符 (:= 空白) + 非空白值（>= 8 字符避免短噪音）。
	// 单引号/双引号/反引号值也吃。Validator 过滤明显占位符（example/your-xxx/foo/bar）
	// 以及值是已知平台 token 形态的（让具体规则保留处置权，避免双重处理）。
	// 排在所有 token-prefix 规则之后，使本规则只兜底「裸 key=value」格式。
	scanner.rules = append(scanner.rules, Rule{
		Name:        "Credential Assignment",
		Pattern:     regexp.MustCompile(`(?i)(?:password|passwd|secret|api[_-]?key|access[_-]?key|private[_-]?key|token)\s*[:=]\s*['"` + "`" + `]?[^\s'"` + "`" + `]{8,}`),
		Replacement: "[CREDENTIAL_REDACTED]",
		Validator:   validCredential,
	})

	// 6. Email - 更精确的模式，需要在电话之前匹配
	scanner.rules = append(scanner.rules, Rule{
		Name:        "Email",
		Pattern:     regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`),
		Replacement: "[EMAIL_REDACTED]",
	})

	// 7. China ID Card - 18 位身份证号，GB11643 校验码验证降误报。
	// 需在 Mobile Phone 之前注册，避免号段前缀被电话规则截走。
	scanner.rules = append(scanner.rules, Rule{
		Name:        "China ID Card",
		Pattern:     regexp.MustCompile(`\b[1-9]\d{5}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[0-9Xx]\b`),
		Replacement: "[CHINA_ID_REDACTED]",
		Validator:   validChinaID,
	})

	// 8. Bank Card - 13-19 位卡号，Luhn 校验降误报（普通数字串不命中）。
	scanner.rules = append(scanner.rules, Rule{
		Name:        "Bank Card",
		Pattern:     regexp.MustCompile(`\b[3-6]\d{12,18}\b`),
		Replacement: "[BANK_CARD_REDACTED]",
		Validator:   validLuhn,
	})

	// 9. Mobile Phone - 放在最后
	// 中国手机号：13x, 14x, 15x, 16x, 17x, 18x, 19x 开头，11位
	// 使用 word boundary 避免匹配密钥中的内部数字
	scanner.rules = append(scanner.rules, Rule{
		Name:        "Mobile Phone",
		Pattern:     regexp.MustCompile(`\b(?:\+?86)?\s*(?:1[3-9]\d{9})\b`),
		Replacement: "[PHONE_REDACTED]",
	})

	return scanner
}

// validLuhn reports whether digits passes the Luhn checksum (ISO/IEC 7812),
// the standard check digit scheme for payment card numbers.
func validLuhn(digits string) bool {
	sum, double := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		c := digits[i]
		if c < '0' || c > '9' {
			return false
		}
		d := int(c - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// idWeights and idCheckCodes implement the GB11643-1999 (ISO 7064 MOD 11-2)
// check digit for 18-digit China resident ID numbers.
var idWeights = [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
var idCheckCodes = [11]byte{'1', '0', 'X', '9', '8', '7', '6', '5', '4', '3', '2'}

// validChinaID reports whether an 18-char China ID number's last character
// matches its GB11643 checksum.
func validChinaID(id string) bool {
	if len(id) != 18 {
		return false
	}
	sum := 0
	for i := 0; i < 17; i++ {
		c := id[i]
		if c < '0' || c > '9' {
			return false
		}
		sum += int(c-'0') * idWeights[i]
	}
	want := idCheckCodes[sum%11]
	got := id[17]
	if got == 'x' {
		got = 'X'
	}
	return got == want
}

// validJWT reports whether a JWT-looking string actually decodes to JSON in its
// header and payload segments. JWTs always start with base64url of `{"..."}`
// (which encodes to `eyJ...`); if either segment fails to decode/parse as a
// JSON object, treat the match as a look-alike and skip it (cut false positives
// from base64 blobs that happen to begin with eyJ by chance).
func validJWT(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return false
	}
	for _, seg := range parts[:2] {
		dec, err := base64.RawURLEncoding.DecodeString(seg)
		if err != nil {
			return false
		}
		// Must start with `{` and end with `}` (a JSON object). JWT headers and
		// payloads are always objects; a string/array/number here means it's
		// not a real JWT.
		t := strings.TrimSpace(string(dec))
		if !strings.HasPrefix(t, "{") || !strings.HasSuffix(t, "}") {
			return false
		}
	}
	return len(parts[2]) >= 8 // signature short floor to drop toy matches
}

// validCredential filters out obvious placeholder values for the
// Credential Assignment rule (examples from docs, dummy configs, etc.). The
// match is the entire "key=value" run; only the value portion (after the first
// `:` or `=`) is inspected. Already-redacted placeholders from upstream rules
// ([XXX_REDACTED]) are also rejected so this rule doesn't double-process what a
// more specific rule (GitHub/OpenAI/etc.) already tokenized.
func validCredential(match string) bool {
	sep := strings.IndexAny(match, ":=")
	if sep < 0 {
		return false
	}
	val := strings.TrimSpace(match[sep+1:])
	val = strings.Trim(val, `"'` + "`")
	if len(val) < 8 {
		return false
	}
	low := strings.ToLower(val)
	switch {
	case strings.Contains(low, "example"),
		strings.Contains(low, "your-"),
		strings.Contains(low, "your_"),
		strings.Contains(low, "<"),
		strings.Contains(low, ">"),
		strings.Contains(low, "xxxxxx"),
		low == "changeme",
		low == "placeholder",
		low == "todo",
		low == "redacted":
		return false
	}
	// Reject values that look like an already-applied redaction marker.
	if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") && strings.Contains(low, "redacted") {
		return false
	}
	return true
}

// Sanitize 清理文本中的所有敏感信息
// 按顺序应用所有规则，返回清理后的文本
func (s *Scanner) Sanitize(input string) string {
	result := input
	for _, rule := range s.rules {
		rule := rule
		result = rule.Pattern.ReplaceAllStringFunc(result, func(match string) string {
			if !rule.valid(match) {
				return match
			}
			return rule.Replacement
		})
	}
	return result
}

// Detect reports the names of rules whose pattern still matches input, without
// modifying it. It is the pre-send leak check for strict-review routes: run it
// on the already-masked, about-to-egress body so any secret a masking rule
// missed is caught before anything leaves the gateway. Empty result = clean.
// extra holds route-scoped rules (mirrors MaskWithExtraRules) so the check
// covers the exact same rule set that masking used.
//
// It also decodes base64-looking runs and re-runs the same rules on the
// plaintext (one level, detection-only): a secret smuggled inside base64 would
// otherwise pass the plain scan untouched. Masking deliberately does NOT
// rewrite inside encoded blobs (it would break the unmask round-trip), so the
// encoded channel is covered here, where a hit blocks egress.
func (s *Scanner) Detect(input string, extra []Rule) []string {
	var hits []string
	check := func(text, suffix string, rules []Rule) {
		for _, rule := range rules {
			for _, match := range rule.Pattern.FindAllString(text, -1) {
				if rule.valid(match) {
					hits = append(hits, rule.Name+suffix)
					break
				}
			}
		}
	}
	check(input, "", s.rules)
	check(input, "", extra)

	for _, cand := range base64CandidatePattern.FindAllString(input, -1) {
		decoded, ok := decodeBase64Loose(cand)
		if !ok || !mostlyText(decoded) {
			continue
		}
		check(string(decoded), " (base64)", s.rules)
		check(string(decoded), " (base64)", extra)
	}
	return hits
}

// base64CandidatePattern matches runs that plausibly carry a base64 payload:
// std or URL-safe alphabet, >= 48 chars (~36 decoded bytes) — long enough to
// hold a secret, high enough a floor to keep ordinary words/IDs out.
var base64CandidatePattern = regexp.MustCompile(`[A-Za-z0-9+/_-]{48,}={0,2}`)

// decodeBase64Loose decodes s as base64, tolerating both the standard and
// URL-safe alphabets and missing padding. Returns ok=false if neither decodes.
func decodeBase64Loose(s string) ([]byte, bool) {
	trimmed := strings.TrimRight(s, "=")
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(trimmed); err == nil {
			return b, true
		}
	}
	return nil, false
}

// mostlyText reports whether decoded bytes look like human-readable text
// (valid UTF-8, >= 90% printable/whitespace runes). Binary blobs — images,
// compressed data — fail this and are skipped, so vision payloads and random
// bytes don't trigger the decoded scan.
func mostlyText(b []byte) bool {
	if len(b) == 0 || !utf8.Valid(b) {
		return false
	}
	total, printable := 0, 0
	for _, r := range string(b) {
		total++
		if unicode.IsPrint(r) || unicode.IsSpace(r) {
			printable++
		}
	}
	return printable*10 >= total*9
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
			// Checksum-validated rules skip look-alike matches (see Rule.Validator).
			if !rule.valid(match) {
				return match
			}
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
