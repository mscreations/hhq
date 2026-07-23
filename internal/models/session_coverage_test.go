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
	"context"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/testutil"
)

// TestSessionStoreDeleteAllForUser covers DeleteAllForUser, previously
// untested: deleting every session belonging to one user (e.g. after a
// password reset) must not touch another user's sessions.
func TestSessionStoreDeleteAllForUser(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	sessions := &SessionStore{DB: conn}
	ctx := t.Context()

	userA, err := users.CreateParent(ctx, "Parent A", "a-delall@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	userB, err := users.CreateParent(ctx, "Parent B", "b-delall@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	tokenA1, err := sessions.Create(ctx, userA, time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tokenA2, err := sessions.Create(ctx, userA, time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	tokenB, err := sessions.Create(ctx, userB, time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := sessions.DeleteAllForUser(ctx, userA); err != nil {
		t.Fatalf("DeleteAllForUser: %v", err)
	}

	if _, err := sessions.Get(ctx, tokenA1); err != ErrNotFound {
		t.Fatalf("Get(tokenA1) after DeleteAllForUser: err = %v, want ErrNotFound", err)
	}
	if _, err := sessions.Get(ctx, tokenA2); err != ErrNotFound {
		t.Fatalf("Get(tokenA2) after DeleteAllForUser: err = %v, want ErrNotFound", err)
	}
	if _, err := sessions.Get(ctx, tokenB); err != nil {
		t.Fatalf("expected user B's session to survive, got err=%v", err)
	}
}

// TestSessionQueriesReturnErrorOnCanceledContext covers Create/Get's
// query-error return branches, otherwise unreachable without breaking the DB
// connection or actually failing crypto/rand.Read (Create's other untested
// branch - see the coverage report notes in the final task summary for why
// that one is left uncovered). See calendar_coverage_test.go's equivalent
// test for the same canceled-context technique/rationale.
func TestSessionQueriesReturnErrorOnCanceledContext(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	sessions := &SessionStore{DB: conn}

	userID, err := users.CreateParent(t.Context(), "Parent", "cancel-ctx@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := sessions.Create(ctx, userID, time.Hour); err == nil {
		t.Error("Create: expected error on canceled context")
	}
	if _, err := sessions.Get(ctx, "any-token"); err == nil {
		t.Error("Get: expected error on canceled context")
	}
}
