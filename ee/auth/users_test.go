// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package auth

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestNormalizeEmail(t *testing.T) {
	cases := map[string]string{
		"  Admin@Acme.IO ": "admin@acme.io",
		"USER@X.COM":       "user@x.com",
		"already@low.com":  "already@low.com",
		"   ":              "",
	}
	for in, want := range cases {
		if got := normalizeEmail(in); got != want {
			t.Errorf("normalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBcryptRoundTrip guards the password-hashing invariants the store relies
// on: a hash verifies against its own password and rejects a wrong one, and the
// plaintext is never recoverable from (equal to) the stored hash.
func TestBcryptRoundTrip(t *testing.T) {
	const pw = "s3cret-passw0rd"
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if string(hash) == pw {
		t.Fatal("stored hash must not equal plaintext")
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte(pw)); err != nil {
		t.Errorf("correct password rejected: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte("wrong")); err == nil {
		t.Error("wrong password accepted")
	}
}
