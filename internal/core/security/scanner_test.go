package security

import (
	"strings"
	"testing"
)

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
