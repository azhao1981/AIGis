package security

import (
	"strings"
	"testing"
)

// MockVaultContext is a mock implementation for testing
type MockVaultContext struct {
	vault map[string]string
}

func (m *MockVaultContext) VaultStore(placeholder, original string) {
	if m.vault == nil {
		m.vault = make(map[string]string)
	}
	m.vault[placeholder] = original
}

func (m *MockVaultContext) VaultGet(placeholder string) (string, bool) {
	if m.vault == nil {
		return "", false
	}
	original, ok := m.vault[placeholder]
	return original, ok
}

func TestMaskPreview(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"test@example.com", "te***om"},
		{"13800138000", "13***00"},
		{"sk-realsecretkey1234567890", "sk***90"},
		{"abcd", "***"}, // length <= 4 fully masked
		{"a", "***"},
		{"", "***"},
		{"abcde", "ab***de"},
	}
	for _, c := range cases {
		if got := maskPreview(c.in); got != c.want {
			t.Errorf("maskPreview(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNewScanner(t *testing.T) {
	scanner := NewScanner()
	if scanner == nil {
		t.Fatal("NewScanner returned nil")
	}

	rules := scanner.GetRules()
	if len(rules) == 0 {
		t.Fatal("Scanner should have at least one rule")
	}

	expectedRules := []string{
		"AWS Access Key",
		"OpenAI API Key",
		"GitHub Token",
		"Google API Key",
		"Private Key",
		"Email",
		"Mobile Phone",
	}

	for _, expected := range expectedRules {
		found := false
		for _, rule := range rules {
			if rule.Name == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected rule %s not found", expected)
		}
	}
}

func TestSanitizeAWS(t *testing.T) {
	scanner := NewScanner()
	input := "My AWS key is AKIAIOSFODNN7EXAMPLE and other text"
	result := scanner.Sanitize(input)

	if !strings.Contains(result, "[AWS_AK_REDACTED]") {
		t.Errorf("Expected AWS key to be redacted, got: %s", result)
	}
	if strings.Contains(result, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS key should be redacted, still present in: %s", result)
	}
}

func TestSanitizeOpenAI(t *testing.T) {
	scanner := NewScanner()

	// Test old format
	input1 := "My key is sk-12345678901234567890abcdef"
	result1 := scanner.Sanitize(input1)
	if !strings.Contains(result1, "[OPENAI_KEY_REDACTED]") {
		t.Errorf("Expected OpenAI key to be redacted, got: %s", result1)
	}

	// Test new proj format
	input2 := "My key is sk-proj-12345678901234567890abcdef"
	result2 := scanner.Sanitize(input2)
	if !strings.Contains(result2, "[OPENAI_KEY_REDACTED]") {
		t.Errorf("Expected OpenAI proj key to be redacted, got: %s", result2)
	}

	// Test with uppercase letters (real format from user's example)
	input3 := "sk-sScxOi4A6BhYh8DY891b1dB95d2f42918a71F50f54C9690b"
	result3 := scanner.Sanitize(input3)
	if !strings.Contains(result3, "[OPENAI_KEY_REDACTED]") {
		t.Errorf("Expected uppercase OpenAI key to be redacted, got: %s", result3)
	}
}

func TestSanitizeGitHub(t *testing.T) {
	scanner := NewScanner()
	input := "Token: ghp_123456789012345678901234567890abcdef"
	result := scanner.Sanitize(input)

	if !strings.Contains(result, "[GITHUB_TOKEN_REDACTED]") {
		t.Errorf("Expected GitHub token to be redacted, got: %s", result)
	}
}

func TestSanitizeGoogle(t *testing.T) {
	scanner := NewScanner()
	input := "Google API: AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWU"
	result := scanner.Sanitize(input)

	if !strings.Contains(result, "[GOOGLE_KEY_REDACTED]") {
		t.Errorf("Expected Google key to be redacted, got: %s", result)
	}
}

func TestSanitizePrivateKey(t *testing.T) {
	scanner := NewScanner()
	input := "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA...\n-----END RSA PRIVATE KEY-----"
	result := scanner.Sanitize(input)

	if !strings.Contains(result, "[PRIVATE_KEY_REDACTED]") {
		t.Errorf("Expected private key to be redacted, got: %s", result)
	}
}

func TestSanitizeEmail(t *testing.T) {
	scanner := NewScanner()
	input := "Contact us at test@example.com or support@company.org"
	result := scanner.Sanitize(input)

	if !strings.Contains(result, "[EMAIL_REDACTED]") {
		t.Errorf("Expected email to be redacted, got: %s", result)
	}
	if strings.Contains(result, "test@example.com") {
		t.Errorf("Email should be redacted, still present in: %s", result)
	}
}

func TestSanitizePhone(t *testing.T) {
	scanner := NewScanner()

	// Test Chinese phones that should be redacted
	tests := []string{
		"13800138000",
		" My phone: 13800138000",
		"+8613800138000",
		"+86 13800138000",
		"Call 13800138000 tomorrow",
	}

	for _, input := range tests {
		result := scanner.Sanitize(input)
		if !strings.Contains(result, "[PHONE_REDACTED]") {
			t.Errorf("Expected phone to be redacted for input '%s', got: %s", input, result)
		}
	}

	// Test that OpenAI keys are not affected by phone rule
	input := "My key is sk-proj-12345678901234567890abcdef"
	result := scanner.Sanitize(input)
	if !strings.Contains(result, "[OPENAI_KEY_REDACTED]") {
		t.Errorf("Expected OpenAI key to be redacted, got: %s", result)
	}
	if strings.Contains(result, "[PHONE_REDACTED]") {
		t.Errorf("OpenAI key should not be treated as phone, got: %s", result)
	}
}

func TestSanitizeMixedSecrets(t *testing.T) {
	scanner := NewScanner()

	// Multiple secrets in one string
	input := `
	Email: user@example.com
	AWS Key: AKIAIOSFODNN7EXAMPLE
	Phone: 13800138000
	OpenAI: sk-12345678901234567890
	`
	result := scanner.Sanitize(input)

	if !strings.Contains(result, "[EMAIL_REDACTED]") {
		t.Error("Email should be redacted")
	}
	if !strings.Contains(result, "[AWS_AK_REDACTED]") {
		t.Error("AWS key should be redacted")
	}
	if !strings.Contains(result, "[PHONE_REDACTED]") {
		t.Error("Phone should be redacted")
	}
	if !strings.Contains(result, "[OPENAI_KEY_REDACTED]") {
		t.Error("OpenAI key should be redacted")
	}

	// Original secrets should not be present
	if strings.Contains(result, "user@example.com") {
		t.Error("Original email should not be present")
	}
	if strings.Contains(result, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("Original AWS key should not be present")
	}
}

func TestAddRule(t *testing.T) {
	scanner := NewScanner()

	// Add custom rule for "SecretCode: XXXX"
	err := scanner.AddRule("SecretCode", `SecretCode:\s*\d{4}-\d{4}`, "[SECRET_CODE_REDACTED]")
	if err != nil {
		t.Fatalf("Failed to add rule: %v", err)
	}

	input := "My secret code is SecretCode: 1234-5678"
	result := scanner.Sanitize(input)

	if !strings.Contains(result, "[SECRET_CODE_REDACTED]") {
		t.Errorf("Custom rule should work, got: %s", result)
	}
}

// TestNewScannerWithRules verifies config-driven custom rules tokenize and
// round-trip like built-ins, and that the built-in rules still apply.
func TestNewScannerWithRules(t *testing.T) {
	scanner, err := NewScannerWithRules([]CustomRule{
		{Name: "ID Card", Pattern: `\b\d{17}[\dXx]\b`},
	})
	if err != nil {
		t.Fatalf("NewScannerWithRules failed: %v", err)
	}

	ctx := &MockVaultContext{}
	const id = "11010519491231002X"
	input := "我的身份证是 " + id + "，邮箱 a@b.com"

	masked := scanner.Mask(ctx, input, nil)
	if strings.Contains(masked, id) {
		t.Errorf("custom ID rule did not tokenize: %s", masked)
	}
	if strings.Contains(masked, "a@b.com") {
		t.Errorf("built-in email rule stopped working: %s", masked)
	}
	if got := scanner.Unmask(ctx, masked); got != input {
		t.Errorf("Unmask() = %q, want %q", got, input)
	}
}

// TestNewScannerWithRulesInvalid confirms a bad regex / empty name fails loud.
func TestNewScannerWithRulesInvalid(t *testing.T) {
	if _, err := NewScannerWithRules([]CustomRule{{Name: "Bad", Pattern: `(unclosed`}}); err == nil {
		t.Error("expected error for invalid regex, got nil")
	}
	if _, err := NewScannerWithRules([]CustomRule{{Name: "", Pattern: `x`}}); err == nil {
		t.Error("expected error for empty rule name, got nil")
	}
}

func TestGetRules(t *testing.T) {
	scanner := NewScanner()

	// Modifying the returned slice should not affect the scanner
	rules := scanner.GetRules()
	originalCount := len(rules)

	// Modify the returned slice
	rules[0].Name = "Modified"

	// Get rules again
	newRules := scanner.GetRules()

	if newRules[0].Name == "Modified" {
		t.Error("GetRules should return a copy, not the original")
	}

	if len(newRules) != originalCount {
		t.Error("Rules count should not change")
	}
}

func TestMaskUnmask(t *testing.T) {
	scanner := NewScanner()
	ctx := &MockVaultContext{}

	testCases := []struct {
		name     string
		input    string
		contains []string // substrings that should be found in masked output
	}{
		{
			name:     "Email",
			input:    "Contact me at test@example.com for details",
			contains: []string{"__AIGIS_SEC_"},
		},
		{
			name:     "Phone",
			input:    "Call me at 13800138000 anytime",
			contains: []string{"__AIGIS_SEC_"},
		},
		{
			name:     "API Key",
			input:    "Use sk-proj-abc123def456789012345 for authentication",
			contains: []string{"__AIGIS_SEC_"},
		},
		{
			name:     "Multiple PII",
			input:    "Email: test@example.com, Phone: 13800138000",
			contains: []string{"__AIGIS_SEC_", "__AIGIS_SEC_"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset vault for each test
			ctx.vault = make(map[string]string)

			// Test Mask
			masked := scanner.Mask(ctx, tc.input, nil)

			// Verify masking occurred
			for _, substr := range tc.contains {
				if !strings.Contains(masked, substr) {
					t.Errorf("Mask() output should contain placeholder pattern")
				}
			}

			// Verify original is NOT in masked output
			if masked == tc.input {
				t.Errorf("Mask() should modify the input")
			}

			// Verify vault has mappings
			if len(ctx.vault) == 0 {
				t.Errorf("Mask() should store mappings in vault")
			}

			// Test Unmask
			unmasked := scanner.Unmask(ctx, masked)

			// Verify unmasking restored the original
			if unmasked != tc.input {
				t.Errorf("Unmask() = %v, want %v", unmasked, tc.input)
			}
		})
	}
}

func TestMaskUnmaskDeterministic(t *testing.T) {
	scanner := NewScanner()
	ctx := &MockVaultContext{}

	input := "test@example.com"

	// First masking
	masked1 := scanner.Mask(ctx, input, nil)

	// Second masking (should use same placeholder)
	masked2 := scanner.Mask(ctx, input, nil)

	// Same input should generate same placeholder
	if masked1 != masked2 {
		t.Errorf("Mask() should be deterministic for same input: %v != %v", masked1, masked2)
	}

	// Verify vault has only one entry (same placeholder)
	if len(ctx.vault) != 1 {
		t.Errorf("Vault should have 1 entry, got %d", len(ctx.vault))
	}
}

func TestMaskWithTags(t *testing.T) {
	scanner := NewScanner()
	ctx := &MockVaultContext{}

	input := "Email: test@example.com, Phone: 13800138000"

	// Mask only Email
	masked := scanner.Mask(ctx, input, []string{"Email"})

	// Should contain placeholder for email
	if !strings.Contains(masked, "__AIGIS_SEC_") {
		t.Errorf("Mask() with tag should mask matching rule")
	}

	// Should still contain phone (not masked)
	if !strings.Contains(masked, "13800138000") {
		t.Errorf("Mask() with specific tag should not mask non-matching rules")
	}

	// Verify vault has only email mapping
	if len(ctx.vault) != 1 {
		t.Errorf("Vault should have 1 entry for email only, got %d", len(ctx.vault))
	}
}

func TestUnmaskWithNoVault(t *testing.T) {
	scanner := NewScanner()

	input := "Some text with __AIGIS_SEC_abc123def456__ placeholder"

	// Unmask with nil context should return input as-is
	result := scanner.Unmask(nil, input)

	if result != input {
		t.Errorf("Unmask() with nil context should return input unchanged")
	}
}

func TestSanitizeBackwardCompatibility(t *testing.T) {
	scanner := NewScanner()

	input := "Contact me at test@example.com for details"
	sanitized := scanner.Sanitize(input)

	// Should use [REDACTED] style placeholders
	if !strings.Contains(sanitized, "[EMAIL_REDACTED]") {
		t.Errorf("Sanitize() should use [REDACTED] placeholders")
	}

	// Should NOT use vault-style placeholders
	if strings.Contains(sanitized, "__AIGIS_SEC_") {
		t.Errorf("Sanitize() should not use vault placeholders")
	}
}

// TestMaskEmailLocalMode verifies the EmailModeLocal option tokenizes only the
// local part of an address while preserving "@domain", and that Unmask still
// reconstructs the full address. Other PII (phone) is unaffected by the option.
func TestMaskEmailLocalMode(t *testing.T) {
	scanner := NewScanner()

	cases := []struct {
		name       string
		input      string
		keepDomain string // domain substring that must survive masking
		gone       string // local-part substring that must be tokenized away
	}{
		{"simple", "mail: dawang@example.com ok", "@example.com", "dawang"},
		{"subdomain", "to a.b@mail.corp.com now", "@mail.corp.com", "a.b@"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &MockVaultContext{}
			masked := scanner.MaskWithOptions(ctx, tc.input, nil, MaskOptions{EmailMode: EmailModeLocal})

			if !strings.Contains(masked, tc.keepDomain) {
				t.Errorf("domain not preserved: %q", masked)
			}
			if strings.Contains(masked, tc.gone) {
				t.Errorf("local part leaked (not tokenized): %q", masked)
			}
			if !strings.Contains(masked, "__AIGIS_SEC_") {
				t.Errorf("local part not tokenized: %q", masked)
			}
			// Vault must store only the local part, never the domain.
			for _, original := range ctx.vault {
				if strings.Contains(original, "@") {
					t.Errorf("vault stored domain too: %q", original)
				}
			}
			// Unmask reconstructs the full original address.
			if got := scanner.Unmask(ctx, masked); got != tc.input {
				t.Errorf("Unmask() = %q, want %q", got, tc.input)
			}
		})
	}
}

// TestMaskEmailFullModeUnchanged confirms the default (full) tokenizes the whole
// address — domain included — so existing behavior is preserved.
func TestMaskEmailFullModeUnchanged(t *testing.T) {
	scanner := NewScanner()
	ctx := &MockVaultContext{}
	const input = "mail: dawang@example.com ok"

	// Default Mask and explicit full mode must behave identically.
	masked := scanner.Mask(ctx, input, nil)
	if strings.Contains(masked, "@example.com") || strings.Contains(masked, "dawang") {
		t.Errorf("full mode should tokenize the whole address: %q", masked)
	}
	if scanner.MaskWithOptions(&MockVaultContext{}, input, nil, MaskOptions{EmailMode: EmailModeFull}) != masked {
		t.Errorf("explicit full mode must equal default Mask")
	}
	if got := scanner.Unmask(ctx, masked); got != input {
		t.Errorf("Unmask() = %q, want %q", got, input)
	}
}
