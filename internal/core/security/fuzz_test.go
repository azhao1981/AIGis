package security

import (
	"strings"
	"testing"
)

// FuzzSanitize hammers the full rule chain (regex + validators + base64
// handling) with arbitrary input. The gateway feeds untrusted request bodies
// straight into these paths, so any panic here is a denial-of-service.
func FuzzSanitize(f *testing.F) {
	seeds := []string{
		"",
		"hello world",
		"sk-realsecretkey1234567890",
		"password=supersecretvalue",
		"api_key: 'quoted-secret-value'",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJlMTIz",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEow==\n-----END RSA PRIVATE KEY-----",
		"AKIAIOSFODNN7EXAMPLE",
		strings.Repeat("A", 100),
		"eyJ" + strings.Repeat(".", 50),
		"token:=:=:=========",
		"\x00\xff\xfe invalid utf8 \x80",
		"用户邮箱 test@example.com 电话 13800138000",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	scanner := NewScanner()
	f.Fuzz(func(t *testing.T, input string) {
		out := scanner.Sanitize(input)
		// Sanitize must never grow output into a placeholder-only echo of a
		// larger input class; the only invariant we can assert generically is
		// no panic and non-nil semantics (empty in -> empty out).
		if input == "" && out != "" {
			t.Errorf("Sanitize(%q) = %q, want empty", input, out)
		}
	})
}

// FuzzMaskUnmaskRoundTrip asserts the core vault invariant on arbitrary input:
// whatever Mask tokenizes, Unmask must restore byte-for-byte. A violation means
// the client would receive corrupted data (or a leaked placeholder).
func FuzzMaskUnmaskRoundTrip(f *testing.F) {
	seeds := []string{
		"email me at test@example.com",
		"key sk-realsecretkey1234567890 and password=hunter2hunter2",
		"api_key=__AIGIS_SEC_abcdef012345__",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJlMTIz done",
		"nested [OPENAI_KEY_REDACTED] marker",
		strings.Repeat("test@example.com ", 20),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	scanner := NewScanner()
	f.Fuzz(func(t *testing.T, input string) {
		// Inputs that already contain placeholder-shaped text would collide
		// with real placeholders on the way back; the gateway never receives
		// those (clients don't know the vault scheme), so skip them.
		if strings.Contains(input, "__AIGIS_SEC_") {
			t.Skip()
		}
		ctx := &MockVaultContext{}
		masked := scanner.Mask(ctx, input, nil)
		restored := scanner.Unmask(ctx, masked)
		if restored != input {
			t.Errorf("round trip broken:\n  in: %q\n out: %q\nmask: %q", input, restored, masked)
		}
	})
}

// FuzzDetect covers the detection path including the base64 second-pass decode
// (candidate extraction + loose decoding + re-scan). Decoding attacker-supplied
// blobs is the classic fuzz target.
func FuzzDetect(f *testing.F) {
	seeds := []string{
		"payload: bXkgYXdzIGtleSBpcyBBS0lBSU9TRk9ETk43RVhBTVBMRSB0aGFua3M=",
		strings.Repeat("QUJD", 50),
		strings.Repeat("=", 100),
		"A" + strings.Repeat("_", 60) + "==",
		"normal text with no secrets at all, just words",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	scanner := NewScanner()
	f.Fuzz(func(t *testing.T, input string) {
		_ = scanner.Detect(input, nil)
	})
}
