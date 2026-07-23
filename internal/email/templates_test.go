package email

import (
	"strings"
	"testing"
)

func TestRenderApprovalEmail(t *testing.T) {
	subject, body, err := RenderApprovalEmail(ApprovalEmailData{
		AppName:    "Custom Family App",
		ChildName:  "Alice",
		ChoreName:  "Take out trash",
		DueDate:    "Mon Aug 1",
		Points:     1,
		ApproveURL: "https://hhq.example.com/approve/abc",
		RejectURL:  "https://hhq.example.com/reject/abc",
	})
	if err != nil {
		t.Fatalf("RenderApprovalEmail: %v", err)
	}
	if subject != "Alice completed: Take out trash" {
		t.Errorf("subject = %q", subject)
	}
	for _, want := range []string{"Alice", "Take out trash", "https://hhq.example.com/approve/abc", "https://hhq.example.com/reject/abc", "Custom Family App"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	// Points == 1 should render singular "point", not "points".
	if !strings.Contains(body, "1 point)") {
		t.Errorf("expected singular 'point' for Points=1, body: %s", body)
	}
}

func TestRenderApprovalEmailPluralPoints(t *testing.T) {
	_, body, err := RenderApprovalEmail(ApprovalEmailData{ChildName: "Alice", ChoreName: "Chore", Points: 5})
	if err != nil {
		t.Fatalf("RenderApprovalEmail: %v", err)
	}
	if !strings.Contains(body, "5 points)") {
		t.Errorf("expected plural 'points' for Points=5, body: %s", body)
	}
}

func TestRenderInviteEmail(t *testing.T) {
	subject, body, err := RenderInviteEmail(InviteEmailData{
		AppName:       "Custom Family App",
		RecipientName: "Bob",
		InviterName:   "Alice",
		AcceptURL:     "https://hhq.example.com/invite/xyz",
	})
	if err != nil {
		t.Fatalf("RenderInviteEmail: %v", err)
	}
	if subject != "You've been invited to Custom Family App" {
		t.Errorf("subject = %q", subject)
	}
	for _, want := range []string{"Bob", "Alice", "https://hhq.example.com/invite/xyz", "Custom Family App"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}
