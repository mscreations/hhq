package auth

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestApprovalLinkSignerRoundTrip(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")

	token := s.Sign(42, ActionApprove, time.Hour)
	id, action, err := s.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id != 42 {
		t.Fatalf("id = %d, want 42", id)
	}
	if action != ActionApprove {
		t.Fatalf("action = %q, want %q", action, ActionApprove)
	}
}

func TestApprovalLinkSignerExpiry(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")

	token := s.Sign(1, ActionReject, -time.Second) // already expired
	if _, _, err := s.Verify(token); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for expired token, got %v", err)
	}
}

func TestApprovalLinkSignerRejectsTamperedToken(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")

	token := s.Sign(7, ActionApprove, time.Hour)

	// Flip a bit in the decoded payload itself (not a base64 character
	// directly) - tampering the raw trailing base64 characters can land on
	// padding bits that base64.RawURLEncoding ignores on decode, which
	// would leave the decoded bytes (and thus the signature check)
	// unchanged and make this test flaky.
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("decoding token: %v", err)
	}
	raw[len(raw)-1] ^= 0xFF
	tampered := base64.RawURLEncoding.EncodeToString(raw)

	if _, _, err := s.Verify(tampered); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for tampered token, got %v", err)
	}
}

func TestApprovalLinkSignerRejectsWrongSecret(t *testing.T) {
	s1 := NewApprovalLinkSigner("secret-one")
	s2 := NewApprovalLinkSigner("secret-two")

	token := s1.Sign(3, ActionApprove, time.Hour)
	if _, _, err := s2.Verify(token); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken when verifying with a different secret, got %v", err)
	}
}

func TestApprovalLinkSignerRejectsMalformedToken(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")

	cases := []string{
		"",
		"not-base64!!!",
		"dGhpcyBpcyBub3QgdmFsaWQ", // valid base64, wrong structure
	}
	for _, c := range cases {
		if _, _, err := s.Verify(c); err != ErrInvalidToken {
			t.Errorf("Verify(%q) error = %v, want ErrInvalidToken", c, err)
		}
	}
}

func TestApprovalLinkSignerInviteAcceptAction(t *testing.T) {
	s := NewApprovalLinkSigner("invite-secret")
	token := s.Sign(99, ActionInviteAccept, time.Hour)

	id, action, err := s.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id != 99 || action != ActionInviteAccept {
		t.Fatalf("got (%d, %q), want (99, %q)", id, action, ActionInviteAccept)
	}
}

func TestApprovalLinkSignerTokenIsURLSafe(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")
	token := s.Sign(1, ActionApprove, time.Hour)

	if strings.ContainsAny(token, "+/=") {
		t.Fatalf("token contains non-URL-safe characters: %q", token)
	}
}

func TestApprovalLinkSignerSignWithContextRoundTrip(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")

	token := s.SignWithContext(42, ActionPasswordReset, time.Hour, "hash-v1")
	id, action, err := s.VerifyWithContext(token, "hash-v1")
	if err != nil {
		t.Fatalf("VerifyWithContext: %v", err)
	}
	if id != 42 {
		t.Fatalf("id = %d, want 42", id)
	}
	if action != ActionPasswordReset {
		t.Fatalf("action = %q, want %q", action, ActionPasswordReset)
	}
}

// TestApprovalLinkSignerVerifyWithContextRejectsChangedContext is the core
// regression test for the password-reset replay fix: once the context a
// token was signed with (e.g. a password hash) changes, the token must be
// rejected even though its signature and expiry are still otherwise valid -
// this is what makes a reset token self-invalidate after its first
// successful use.
func TestApprovalLinkSignerVerifyWithContextRejectsChangedContext(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")

	token := s.SignWithContext(42, ActionPasswordReset, time.Hour, "hash-v1")
	if _, _, err := s.VerifyWithContext(token, "hash-v2"); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken when the context no longer matches, got %v", err)
	}
}

func TestApprovalLinkSignerVerifyWithContextRejectsExpiredToken(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")

	token := s.SignWithContext(1, ActionPasswordReset, -time.Second, "hash-v1")
	if _, _, err := s.VerifyWithContext(token, "hash-v1"); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for expired token, got %v", err)
	}
}

func TestApprovalLinkSignerVerifyWithContextRejectsPlainSignToken(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")

	// A token produced by the plain (context-less) Sign has 4 dot-separated
	// parts instead of 5; VerifyWithContext must reject it as malformed
	// rather than panicking on an out-of-range slice index.
	token := s.Sign(1, ActionApprove, time.Hour)
	if _, _, err := s.VerifyWithContext(token, "any-context"); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for a plain-Sign token, got %v", err)
	}
}

func TestPeekIDExtractsUnverifiedID(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")
	token := s.SignWithContext(123, ActionPasswordReset, time.Hour, "hash-v1")

	id, ok := PeekID(token)
	if !ok {
		t.Fatal("PeekID: expected ok=true")
	}
	if id != 123 {
		t.Fatalf("id = %d, want 123", id)
	}
}

func TestPeekIDRejectsMalformedToken(t *testing.T) {
	cases := []string{"", "not-base64!!!"}
	for _, c := range cases {
		if _, ok := PeekID(c); ok {
			t.Errorf("PeekID(%q) ok = true, want false", c)
		}
	}
}

// TestApprovalLinkSignerVerifyRejectsNonNumericID exercises Verify's id
// strconv.Atoi failure path (a token with a validly-signed but non-numeric
// id field) - forged by hand rather than via Sign, since Sign always
// produces a numeric id.
func TestApprovalLinkSignerVerifyRejectsNonNumericID(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")

	exp := time.Now().Add(time.Hour).Unix()
	payload := fmt.Sprintf("%s.%s.%d", "not-an-id", ActionApprove, exp)
	raw := payload + "." + s.mac(payload)
	token := base64.RawURLEncoding.EncodeToString([]byte(raw))

	if _, _, err := s.Verify(token); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for non-numeric id, got %v", err)
	}
}

// TestApprovalLinkSignerVerifyWithContextRejectsMalformedBase64 exercises
// VerifyWithContext's base64-decode failure path directly (distinct from
// TestApprovalLinkSignerRejectsMalformedToken, which only covers the
// non-context Verify).
func TestApprovalLinkSignerVerifyWithContextRejectsMalformedBase64(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")

	if _, _, err := s.VerifyWithContext("not-base64!!!", "any-context"); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for malformed base64, got %v", err)
	}
}

// TestApprovalLinkSignerVerifyWithContextRejectsTamperedSignature exercises
// VerifyWithContext's own signature-mismatch branch (distinct from the
// plain-Verify tamper test above, since VerifyWithContext has a separate
// signature check over a 5-part payload).
func TestApprovalLinkSignerVerifyWithContextRejectsTamperedSignature(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")

	token := s.SignWithContext(42, ActionPasswordReset, time.Hour, "hash-v1")
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("decoding token: %v", err)
	}
	raw[len(raw)-1] ^= 0xFF
	tampered := base64.RawURLEncoding.EncodeToString(raw)

	if _, _, err := s.VerifyWithContext(tampered, "hash-v1"); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for tampered token, got %v", err)
	}
}

// TestApprovalLinkSignerVerifyWithContextRejectsNonNumericID exercises
// VerifyWithContext's id strconv.Atoi failure path, mirroring
// TestApprovalLinkSignerVerifyRejectsNonNumericID but for the 5-part
// context-bound payload shape.
func TestApprovalLinkSignerVerifyWithContextRejectsNonNumericID(t *testing.T) {
	s := NewApprovalLinkSigner("test-secret")

	exp := time.Now().Add(time.Hour).Unix()
	ctxHash := s.mac("ctx:hash-v1")
	payload := fmt.Sprintf("%s.%s.%d.%s", "not-an-id", ActionPasswordReset, exp, ctxHash)
	raw := payload + "." + s.mac(payload)
	token := base64.RawURLEncoding.EncodeToString([]byte(raw))

	if _, _, err := s.VerifyWithContext(token, "hash-v1"); err != ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for non-numeric id, got %v", err)
	}
}
