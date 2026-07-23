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
