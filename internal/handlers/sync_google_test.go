package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
	"github.com/mscreations/hhq/internal/util"
)

// TestSyncAccountAsyncGoogleRecordsMarkSynced is a direct regression test for
// syncAccountAsync's Google branch: previously a Google account fell through
// to the CalDAV path's unconditional EncryptedPassword decrypt (nil for a
// Google row) and, even after being special-cased, its "provider not yet
// supported" default branch never called MarkSynced at all - so a Google
// account's dashboard row sat at "not yet synced" forever with no feedback.
// This confirms MarkSynced is now always called, recording the (expected,
// since the refresh token here is deliberately invalid) failure.
func TestSyncAccountAsyncGoogleRecordsMarkSynced(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	encryptor, err := util.NewEncryptor(strings.Repeat("cd", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	accounts := &models.CalendarAccountStore{DB: conn}
	a := &App{
		Cfg:              &config.Config{CalendarWindowDays: 7, PublicBaseURL: "http://testserver.local"},
		CalendarAccounts: accounts,
		Calendars:        &models.CalendarStore{DB: conn},
		Events:           &models.EventStore{DB: conn},
		Encryptor:        encryptor,
	}

	accountID, err := accounts.CreateGoogle(ctx, "Broken Google Account", []byte("not-valid-ciphertext"), "me@example.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}

	a.syncAccountAsync(accountID)

	deadline := time.Now().Add(5 * time.Second)
	for a.syncing.isSyncing(accountID) {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for background sync to finish")
		}
		time.Sleep(10 * time.Millisecond)
	}

	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !account.LastSyncError.Valid {
		t.Fatal("expected the bad refresh token to be recorded as a sync error via MarkSynced")
	}
}
