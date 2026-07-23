package models

import (
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/testutil"
)

func TestSessionStoreCreateAndGet(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	sessions := &SessionStore{DB: conn}
	ctx := t.Context()

	userID, err := users.CreateParent(ctx, "Parent", "p@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	token, err := sessions.Create(ctx, userID, time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}

	got, err := sessions.Get(ctx, token)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UserID != userID {
		t.Fatalf("UserID = %d, want %d", got.UserID, userID)
	}
}

func TestSessionStoreGetUnknownToken(t *testing.T) {
	conn := testutil.RequireDB(t)
	sessions := &SessionStore{DB: conn}

	if _, err := sessions.Get(t.Context(), "does-not-exist"); err != ErrNotFound {
		t.Fatalf("Get unknown token: err = %v, want ErrNotFound", err)
	}
}

func TestSessionStoreGetExpiredTokenActsAsNotFound(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	sessions := &SessionStore{DB: conn}
	ctx := t.Context()

	userID, err := users.CreateParent(ctx, "Parent", "p2@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	token, err := sessions.Create(ctx, userID, -time.Hour) // already expired
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := sessions.Get(ctx, token); err != ErrNotFound {
		t.Fatalf("Get expired token: err = %v, want ErrNotFound", err)
	}
}

func TestSessionStoreDelete(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	sessions := &SessionStore{DB: conn}
	ctx := t.Context()

	userID, err := users.CreateParent(ctx, "Parent", "p3@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	token, err := sessions.Create(ctx, userID, time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := sessions.Delete(ctx, token); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := sessions.Get(ctx, token); err != ErrNotFound {
		t.Fatalf("Get after delete: err = %v, want ErrNotFound", err)
	}
}

func TestSessionStoreDeleteExpired(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	sessions := &SessionStore{DB: conn}
	ctx := t.Context()

	userID, err := users.CreateParent(ctx, "Parent", "p4@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	expiredToken, err := sessions.Create(ctx, userID, -time.Hour)
	if err != nil {
		t.Fatalf("Create expired: %v", err)
	}
	activeToken, err := sessions.Create(ctx, userID, time.Hour)
	if err != nil {
		t.Fatalf("Create active: %v", err)
	}

	if err := sessions.DeleteExpired(ctx); err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}

	var count int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM hhq_sessions WHERE token = $1`, expiredToken).Scan(&count); err != nil {
		t.Fatalf("querying expired token row: %v", err)
	}
	if count != 0 {
		t.Fatal("expected expired session row to be deleted")
	}

	if _, err := sessions.Get(ctx, activeToken); err != nil {
		t.Fatalf("expected active session to survive DeleteExpired, got err=%v", err)
	}
}
