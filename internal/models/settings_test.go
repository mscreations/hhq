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
