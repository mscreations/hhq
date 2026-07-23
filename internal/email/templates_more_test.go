package email

import (
	"strings"
	"testing"
)

// TestRenderPasswordResetEmail covers RenderPasswordResetEmail, which
// (per grep across the repo) has a real caller (internal/auth's password
// reset flow) but had zero existing test coverage - templates_test.go
// (existing, not modified here) only exercised RenderApprovalEmail and
// RenderInviteEmail.
func TestRenderPasswordResetEmail(t *testing.T) {
	subject, body, err := RenderPasswordResetEmail(PasswordResetEmailData{
		AppName:       "Custom Family App",
		RecipientName: "Carol",
		ResetURL:      "https://hhq.example.com/reset/def",
	})
	if err != nil {
		t.Fatalf("RenderPasswordResetEmail: %v", err)
	}
	if subject != "Reset your Custom Family App password" {
		t.Errorf("subject = %q", subject)
	}
	for _, want := range []string{"Carol", "https://hhq.example.com/reset/def", "Custom Family App"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}
