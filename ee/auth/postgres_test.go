package auth

import "testing"

func TestHashKey(t *testing.T) {
	// Deterministic: same input -> same digest.
	a := hashKey("secret-key")
	b := hashKey("secret-key")
	if a != b {
		t.Fatalf("hashKey not deterministic: %q != %q", a, b)
	}

	// Known SHA-256 hex of "secret-key".
	const want = "85dbe15d75ef9308c7ae0f33c7a324cc6f4bf519a2ed2f3027bd33c140a4f9aa"
	if a != want {
		t.Fatalf("hashKey(secret-key) = %q, want %q", a, want)
	}

	// Different inputs -> different digests.
	if hashKey("a") == hashKey("b") {
		t.Fatal("distinct inputs produced identical hash")
	}

	// SHA-256 hex digest is always 64 chars.
	if got := hashKey(""); len(got) != 64 {
		t.Fatalf("digest length = %d, want 64", len(got))
	}
}
