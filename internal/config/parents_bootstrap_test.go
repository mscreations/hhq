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

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseParentsBootstrapValidJSON(t *testing.T) {
	raw := `[
		{"name": "My Name", "display_name": "Dad", "email": "myemail@mydomain.com", "password": "mypassword", "color": "green", "avatar_file": "/config/avatars/mypic.jpg"},
		{"name": "Other Parent", "email": "other@example.com", "password_file": "/secrets/other-password"}
	]`

	entries, err := ParseParentsBootstrap(raw)
	if err != nil {
		t.Fatalf("ParseParentsBootstrap: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Name != "My Name" || entries[0].DisplayName != "Dad" || entries[0].Email != "myemail@mydomain.com" || entries[0].Color != "green" || entries[0].AvatarFile != "/config/avatars/mypic.jpg" {
		t.Fatalf("entries[0] = %+v", entries[0])
	}
	if entries[1].PasswordFile != "/secrets/other-password" {
		t.Fatalf("entries[1].PasswordFile = %q", entries[1].PasswordFile)
	}
}

func TestParseParentsBootstrapEmptyArray(t *testing.T) {
	entries, err := ParseParentsBootstrap(`[]`)
	if err != nil {
		t.Fatalf("ParseParentsBootstrap: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0", len(entries))
	}
}

func TestParseParentsBootstrapInvalidJSON(t *testing.T) {
	if _, err := ParseParentsBootstrap(`not json`); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestParentBootstrapResolveEmailUsesEmailWhenSet(t *testing.T) {
	e := ParentBootstrap{Email: "me@example.com"}
	got, err := e.ResolveEmail()
	if err != nil {
		t.Fatalf("ResolveEmail: %v", err)
	}
	if got != "me@example.com" {
		t.Fatalf("ResolveEmail() = %q, want %q", got, "me@example.com")
	}
}

func TestParentBootstrapResolveEmailReadsAndTrimsEmailFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "email")
	if err := os.WriteFile(path, []byte("me@example.com\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	e := ParentBootstrap{EmailFile: path}
	got, err := e.ResolveEmail()
	if err != nil {
		t.Fatalf("ResolveEmail: %v", err)
	}
	if got != "me@example.com" {
		t.Fatalf("ResolveEmail() = %q, want %q", got, "me@example.com")
	}
}

func TestParentBootstrapResolveEmailRejectsBothSet(t *testing.T) {
	e := ParentBootstrap{Email: "me@example.com", EmailFile: "/some/path"}
	if _, err := e.ResolveEmail(); err == nil {
		t.Fatal("expected an error when both email and email_file are set")
	}
}

func TestParentBootstrapResolveEmailEmptyWhenNeitherSet(t *testing.T) {
	e := ParentBootstrap{}
	got, err := e.ResolveEmail()
	if err != nil {
		t.Fatalf("ResolveEmail: %v", err)
	}
	if got != "" {
		t.Fatalf("ResolveEmail() = %q, want empty", got)
	}
}

func TestParentBootstrapResolvePasswordUsesPasswordWhenSet(t *testing.T) {
	e := ParentBootstrap{Password: "mypassword"}
	got, err := e.ResolvePassword()
	if err != nil {
		t.Fatalf("ResolvePassword: %v", err)
	}
	if got != "mypassword" {
		t.Fatalf("ResolvePassword() = %q, want %q", got, "mypassword")
	}
}

func TestParentBootstrapResolvePasswordReadsAndTrimsPasswordFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("mypassword\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	e := ParentBootstrap{PasswordFile: path}
	got, err := e.ResolvePassword()
	if err != nil {
		t.Fatalf("ResolvePassword: %v", err)
	}
	if got != "mypassword" {
		t.Fatalf("ResolvePassword() = %q, want %q", got, "mypassword")
	}
}

func TestParentBootstrapResolvePasswordRejectsBothSet(t *testing.T) {
	e := ParentBootstrap{Password: "mypassword", PasswordFile: "/some/path"}
	if _, err := e.ResolvePassword(); err == nil {
		t.Fatal("expected an error when both password and password_file are set")
	}
}

func TestParentBootstrapResolvePasswordErrorsOnUnreadableFile(t *testing.T) {
	e := ParentBootstrap{PasswordFile: filepath.Join(t.TempDir(), "does-not-exist")}
	if _, err := e.ResolvePassword(); err == nil {
		t.Fatal("expected an error for a missing password_file")
	}
}
