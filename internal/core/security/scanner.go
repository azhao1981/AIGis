package security

import (
	"regexp"
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
