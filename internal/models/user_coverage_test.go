package models

import (
	"context"
	"testing"

	"github.com/mscreations/hhq/internal/testutil"
)

// TestUserStoreNextAvailableColorCyclesOncePaletteExhausted covers the
// modulo-cycling fallback in UserStore.NextAvailableColor - reached only once
// every palette color is already assigned to some user. Mirrors
// CalendarStore's equivalent cycling test.
func TestUserStoreNextAvailableColorCyclesOncePaletteExhausted(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	for i, c := range childColorPalette {
		if _, err := s.CreateChild(ctx, "Kid", c); err != nil {
			t.Fatalf("CreateChild(%d): %v", i, err)
		}
	}

	got, err := s.NextAvailableColor(ctx)
	if err != nil {
		t.Fatalf("NextAvailableColor: %v", err)
	}
	// len(used) == len(childColorPalette), so the modulo wraps back to index 0.
	if got != childColorPalette[0] {
		t.Fatalf("NextAvailableColor once exhausted = %q, want cycled color %q", got, childColorPalette[0])
	}
}

// TestUserStoreSetPassword covers both branches of SetPassword: updating an
// existing parent's password, and ErrNotFound when the id doesn't match any
// parent row (e.g. deleted between verification and the call).
func TestUserStoreSetPassword(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	id, err := s.CreateParent(ctx, "Parent", "setpw@example.com", "old-hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	if err := s.SetPassword(ctx, id, "new-hash"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	got, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.PasswordHash.String != "new-hash" {
		t.Fatalf("SetPassword did not apply: %+v", got.PasswordHash)
	}
}

func TestUserStoreSetPasswordNotFound(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}

	if err := s.SetPassword(t.Context(), 99999, "new-hash"); err != ErrNotFound {
		t.Fatalf("SetPassword for missing id: err = %v, want ErrNotFound", err)
	}
}

// TestUserQueriesReturnErrorOnCanceledContext covers the query-error return
// branches in ListChildren/ListParents/ListAll, otherwise unreachable
// without breaking the DB connection - see calendar_coverage_test.go's
// equivalent test for the same technique/rationale.
func TestUserQueriesReturnErrorOnCanceledContext(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := s.ListChildren(ctx); err == nil {
		t.Error("ListChildren: expected error on canceled context")
	}
	if _, err := s.ListParents(ctx); err == nil {
		t.Error("ListParents: expected error on canceled context")
	}
	if _, err := s.ListAll(ctx); err == nil {
		t.Error("ListAll: expected error on canceled context")
	}
}
