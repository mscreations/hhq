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

	"github.com/mscreations/hhq/internal/models"
)

func TestParseCalendarAccountsBootstrapValidJSON(t *testing.T) {
	raw := `[
		{"name": "Mom's Fastmail", "provider": "fastmail", "username": "mom@fastmail.com", "password": "app-pass"},
		{"name": "Generic", "provider": "caldav_generic", "url": "https://caldav.example.com/", "username": "u", "password": "p"}
	]`

	entries, err := ParseCalendarAccountsBootstrap(raw)
	if err != nil {
		t.Fatalf("ParseCalendarAccountsBootstrap: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Name != "Mom's Fastmail" || entries[0].Provider != "fastmail" {
		t.Fatalf("entries[0] = %+v", entries[0])
	}
	if entries[1].URL != "https://caldav.example.com/" {
		t.Fatalf("entries[1].URL = %q", entries[1].URL)
	}
}

func TestParseCalendarAccountsBootstrapEmptyArray(t *testing.T) {
	entries, err := ParseCalendarAccountsBootstrap(`[]`)
	if err != nil {
		t.Fatalf("ParseCalendarAccountsBootstrap: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0", len(entries))
	}
}

func TestParseCalendarAccountsBootstrapInvalidJSON(t *testing.T) {
	if _, err := ParseCalendarAccountsBootstrap(`not json`); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestResolvePasswordUsesPasswordWhenSet(t *testing.T) {
	e := CalendarAccountBootstrap{Password: "app-pass"}
	got, err := e.ResolvePassword()
	if err != nil {
		t.Fatalf("ResolvePassword: %v", err)
	}
	if got != "app-pass" {
		t.Fatalf("ResolvePassword() = %q, want %q", got, "app-pass")
	}
}

func TestResolvePasswordReadsAndTrimsPasswordFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("app-pass\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	e := CalendarAccountBootstrap{PasswordFile: path}
	got, err := e.ResolvePassword()
	if err != nil {
		t.Fatalf("ResolvePassword: %v", err)
	}
	if got != "app-pass" {
		t.Fatalf("ResolvePassword() = %q, want %q", got, "app-pass")
	}
}

func TestResolvePasswordRejectsBothSet(t *testing.T) {
	e := CalendarAccountBootstrap{Password: "app-pass", PasswordFile: "/some/path"}
	if _, err := e.ResolvePassword(); err == nil {
		t.Fatal("expected an error when both password and password_file are set")
	}
}

func TestResolvePasswordErrorsOnUnreadableFile(t *testing.T) {
	e := CalendarAccountBootstrap{PasswordFile: filepath.Join(t.TempDir(), "does-not-exist")}
	if _, err := e.ResolvePassword(); err == nil {
		t.Fatal("expected an error for a missing password_file")
	}
}

func TestResolvePasswordEmptyWhenNeitherSet(t *testing.T) {
	e := CalendarAccountBootstrap{}
	got, err := e.ResolvePassword()
	if err != nil {
		t.Fatalf("ResolvePassword: %v", err)
	}
	if got != "" {
		t.Fatalf("ResolvePassword() = %q, want empty", got)
	}
}

func TestResolveProvider(t *testing.T) {
	cases := []struct {
		in      string
		want    models.CalendarProvider
		wantErr bool
	}{
		{"fastmail", models.ProviderFastmail, false},
		{"Fastmail", models.ProviderFastmail, false},
		{"caldav_fastmail", models.ProviderFastmail, false},
		{"icloud", models.ProviderICloud, false},
		{"caldav_icloud", models.ProviderICloud, false},
		{"generic", models.ProviderGeneric, false},
		{"caldav", models.ProviderGeneric, false},
		{"caldav_generic", models.ProviderGeneric, false},
		{"google", "", true},
		{"", "", true},
		{"bogus", "", true},
	}
	for _, c := range cases {
		got, err := ResolveProvider(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ResolveProvider(%q) = %q, nil; want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ResolveProvider(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ResolveProvider(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
