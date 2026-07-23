package auth

import (
	"strings"
	"testing"
)

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if !CheckPassword(hash, "correct horse battery staple") {
		t.Fatal("expected CheckPassword to accept the correct plaintext")
	}
	if CheckPassword(hash, "wrong password") {
		t.Fatal("expected CheckPassword to reject an incorrect plaintext")
	}
}

func TestCheckPasswordTiming(t *testing.T) {
	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if !CheckPasswordTiming(hash, "s3cret") {
		t.Fatal("expected match against a real hash to succeed")
	}
	if CheckPasswordTiming(hash, "wrong") {
		t.Fatal("expected mismatch against a real hash to fail")
	}
	if CheckPasswordTiming("", "anything") {
		t.Fatal("expected empty hash (unknown account) to always return false")
	}
}

// TestMustHashPasswordPanicsOnHashError exercises mustHashPassword's panic
// path (the logic factored out of init() so the dummy-hash precomputation
// failure branch - normally unreachable, since init()'s fixed placeholder
// string never fails to hash - can be tested directly). bcrypt rejects any
// plaintext over 72 bytes, which is a reliable, deterministic way to force
// HashPassword to return an error without mocking bcrypt itself.
func TestMustHashPasswordPanicsOnHashError(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected mustHashPassword to panic on a hash error")
		}
		msg, ok := r.(string)
		if !ok || !strings.HasPrefix(msg, "auth: failed to precompute dummy bcrypt hash: ") {
			t.Fatalf("panic value = %v, want prefix %q", r, "auth: failed to precompute dummy bcrypt hash: ")
		}
	}()

	tooLong := strings.Repeat("a", 73)
	mustHashPassword(tooLong)
}
