package scheduler

import (
	"strings"
	"testing"

	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
	"github.com/mscreations/hhq/internal/util"
)

// TestSyncAllAccountsGoogleDecryptsRefreshTokenNotPassword is a direct
// regression test for the dispatch-site fix in syncAllAccounts: a
// ProviderGoogle account has a nil EncryptedPassword (it authenticates via
// OAuth2 refresh token instead), so the old code path - which unconditionally
// decrypted EncryptedPassword before dispatching on provider - would have
// failed decrypting a nil/empty ciphertext for every Google account. This
// confirms the Google branch reads EncryptedRefreshToken instead, and that a
// bad refresh token is recorded via MarkSynced rather than panicking on the
// nil password.
func TestSyncAllAccountsGoogleDecryptsRefreshTokenNotPassword(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	encryptor, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	accounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}

	// Deliberately garbage ciphertext (not this encryptor's own output) so
	// Decrypt fails fast without ever needing network access to Google's
	// token endpoint - the point of this test is the dispatch/field choice,
	// not a live OAuth2 exchange.
	accountID, err := accounts.CreateGoogle(ctx, "Broken Google Account", []byte("not-valid-ciphertext"), "me@example.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}

	s := &Scheduler{
		Cfg:              &config.Config{CalendarWindowDays: 7},
		CalendarAccounts: accounts,
		Calendars:        calendars,
		Events:           events,
		Encryptor:        encryptor,
	}

	// Must not panic despite EncryptedPassword being nil for this account.
	s.syncAllAccounts(ctx)

	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !account.LastSyncError.Valid {
		t.Fatal("expected the bad refresh token to be recorded as a sync error via MarkSynced")
	}
}
