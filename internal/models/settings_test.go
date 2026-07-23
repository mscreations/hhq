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

package models

import (
	"testing"

	"github.com/mscreations/hhq/internal/testutil"
)

func TestSettingsStoreGetFallbackWhenMissing(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &SettingsStore{DB: conn}

	got, err := s.Get(t.Context(), "does_not_exist", "fallback-value")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "fallback-value" {
		t.Fatalf("Get() = %q, want fallback", got)
	}
}

func TestSettingsStoreSetAndGet(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &SettingsStore{DB: conn}
	ctx := t.Context()

	if err := s.Set(ctx, "app_title", "My Family"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Get(ctx, "app_title", "fallback")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "My Family" {
		t.Fatalf("Get() = %q, want %q", got, "My Family")
	}
}

func TestSettingsStoreSetUpsertsExistingKey(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &SettingsStore{DB: conn}
	ctx := t.Context()

	if err := s.Set(ctx, "timezone", "UTC"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Set(ctx, "timezone", "America/Chicago"); err != nil {
		t.Fatalf("Set (overwrite): %v", err)
	}

	got, err := s.Get(ctx, "timezone", "fallback")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "America/Chicago" {
		t.Fatalf("Get() = %q, want updated value %q", got, "America/Chicago")
	}
}
