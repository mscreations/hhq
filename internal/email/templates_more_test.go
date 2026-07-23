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
