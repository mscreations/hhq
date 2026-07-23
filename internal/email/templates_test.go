// Copyright (C) 2026 Jon Shaulis
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

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
