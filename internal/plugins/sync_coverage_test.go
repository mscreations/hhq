package plugins

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
	"github.com/mscreations/hhq/internal/util"
)

// newTestEncryptor builds a real AES-256-GCM Encryptor with a fixed key -
// mirrors internal/scheduler/plugin_sync_test.go's pattern.
func newTestEncryptor(t *testing.T) *util.Encryptor {
	t.Helper()
	enc, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	return enc
}

// newEventsServer serves GET /events with a canned JSON body - used across
// the SyncOne tests below.
func newEventsServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setupPluginAndCalendar creates a real calendar_accounts + calendars row
// (provider=plugin) and a hhq_plugins row pointing baseURL/token at it -
// returns the plugin, calendar id and account id for assertions.
func setupPluginAndCalendar(t *testing.T, sc SyncContext, baseURL, rawToken string) (models.Plugin, int, int) {
	t.Helper()
	ctx := t.Context()

	accountID, err := sc.CalendarAccounts.Create(ctx, models.CalendarAccount{
		Name:             "Plugin: Test",
		Provider:         models.ProviderPlugin,
		BootstrapManaged: true,
	})
	if err != nil {
		t.Fatalf("CalendarAccounts.Create: %v", err)
	}
	calendarID, err := sc.Calendars.UpsertDiscovered(ctx, accountID, "test-plugin", "Test Plugin", "#3B82F6")
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}

	var encToken []byte
	if rawToken != "" {
		encToken, err = sc.Encryptor.Encrypt(rawToken)
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
	} else {
		// Deliberately not a valid AES-GCM ciphertext (too short/garbage),
		// so Decrypt fails - exercises SyncOne's decrypt-error branch.
		encToken = []byte("not-a-valid-ciphertext")
	}

	p := models.Plugin{
		ID:               "test-plugin",
		Name:             "Test Plugin",
		BaseURL:          baseURL,
		Enabled:          true,
		BootstrapManaged: true,
		CalendarID:       sql.NullInt32{Int32: int32(calendarID), Valid: true},
		EncryptedToken:   encToken,
	}
	if err := sc.Plugins.Create(t.Context(), p); err != nil {
		t.Fatalf("Plugins.Create: %v", err)
	}
	if err := sc.Plugins.SetToken(t.Context(), p.ID, encToken); err != nil {
		t.Fatalf("Plugins.SetToken: %v", err)
	}
	if err := sc.Plugins.SetCalendarID(t.Context(), p.ID, calendarID); err != nil {
		t.Fatalf("Plugins.SetCalendarID: %v", err)
	}
	got, err := sc.Plugins.GetByID(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Plugins.GetByID: %v", err)
	}
	return *got, calendarID, accountID
}

func newSyncContext(t *testing.T) SyncContext {
	t.Helper()
	db := testutil.RequireDB(t)
	return SyncContext{
		Plugins:          &models.PluginStore{DB: db},
		Calendars:        &models.CalendarStore{DB: db},
		Events:           &models.EventStore{DB: db},
		CalendarAccounts: &models.CalendarAccountStore{DB: db},
		Encryptor:        newTestEncryptor(t),
	}
}

// TestSyncOneSuccessUpsertsEventsAndPrunesStale drives SyncOne fully
// end-to-end against a real Postgres: two synthetic events (one carrying an
// action, exercising convertActions' non-empty branch, one without), a
// pre-existing stale cached event that must be pruned, and confirms
// MarkHealth/Calendars.MarkSynced/CalendarAccounts.MarkSynced (accountID != 0
// branch) are all reached without error.
func TestSyncOneSuccessUpsertsEventsAndPrunesStale(t *testing.T) {
	sc := newSyncContext(t)
	ctx := t.Context()

	body := `{"events":[
		{"uid":"ev-with-action","summary":"Electric bill","all_day":true,
		 "starts_at":"` + time.Now().Add(24*time.Hour).Format(time.RFC3339) + `",
		 "ends_at":"` + time.Now().Add(25*time.Hour).Format(time.RFC3339) + `",
		 "actions":[{"id":"mark-paid","label":"Mark Paid","requires_parent":true}]},
		{"uid":"ev-no-action","summary":"Water bill","all_day":true,
		 "starts_at":"` + time.Now().Add(48*time.Hour).Format(time.RFC3339) + `",
		 "ends_at":"` + time.Now().Add(49*time.Hour).Format(time.RFC3339) + `"}
	]}`
	srv := newEventsServer(t, body, 0)

	p, calendarID, accountID := setupPluginAndCalendar(t, sc, srv.URL, "test-token")

	// Seed a stale cached event under the same calendar that the plugin no
	// longer reports - SyncOne's PruneStale call must remove it.
	if err := sc.Events.Upsert(ctx, models.Event{
		CalendarID: calendarID,
		UID:        "stale-event",
		Summary:    "Old",
		StartsAt:   time.Now(),
		EndsAt:     time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seeding stale event: %v", err)
	}

	if err := sc.SyncOne(ctx, p, 7); err != nil {
		t.Fatalf("SyncOne: %v", err)
	}

	events, err := sc.Events.ListInWindow(ctx, 7)
	if err != nil {
		t.Fatalf("ListInWindow: %v", err)
	}
	var found, foundNoAction, foundStale bool
	var withActionID int
	for _, e := range events {
		switch e.UID {
		case "ev-with-action":
			found = true
			withActionID = e.ID
		case "ev-no-action":
			foundNoAction = true
		case "stale-event":
			foundStale = true
		}
	}
	if !found || !foundNoAction {
		t.Fatalf("expected both synced events present, got %+v", events)
	}
	if foundStale {
		t.Fatal("expected the stale event to be pruned")
	}

	// ListInWindow deliberately doesn't select the actions column (see
	// EventStore.GetByID's doc comment) - fetch the full row to confirm
	// convertActions' non-empty branch actually persisted the action.
	full, err := sc.Events.GetByID(ctx, withActionID)
	if err != nil {
		t.Fatalf("Events.GetByID: %v", err)
	}
	if len(full.Actions) != 1 || full.Actions[0].ID != "mark-paid" || !full.Actions[0].RequiresParent {
		t.Fatalf("expected the action to round-trip via convertActions, got %+v", full.Actions)
	}

	updatedPlugin, err := sc.Plugins.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("Plugins.GetByID: %v", err)
	}
	if !updatedPlugin.LastHealthyAt.Valid || updatedPlugin.LastError.Valid {
		t.Fatalf("expected MarkHealth(nil) to record success, got %+v", updatedPlugin)
	}

	account, err := sc.CalendarAccounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("CalendarAccounts.GetByID: %v", err)
	}
	if !account.LastSyncedAt.Valid || account.LastSyncError.Valid {
		t.Fatalf("expected the owning account's sync status to be marked healthy, got %+v", account)
	}
}

// TestSyncOneDecryptErrorWithAccount exercises the token-decrypt-failure
// branch when the synthetic calendar row *does* resolve to an owning
// account (accountID != 0), so CalendarAccounts.MarkSynced is also reached.
func TestSyncOneDecryptErrorWithAccount(t *testing.T) {
	sc := newSyncContext(t)
	ctx := t.Context()

	srv := newEventsServer(t, `{"events":[]}`, 0)
	p, _, accountID := setupPluginAndCalendar(t, sc, srv.URL, "") // "" => garbage ciphertext

	err := sc.SyncOne(ctx, p, 7)
	if err == nil {
		t.Fatal("expected a decrypt error")
	}

	updatedPlugin, err2 := sc.Plugins.GetByID(ctx, p.ID)
	if err2 != nil {
		t.Fatalf("Plugins.GetByID: %v", err2)
	}
	if !updatedPlugin.LastError.Valid {
		t.Fatalf("expected MarkHealth to record the decrypt error, got %+v", updatedPlugin)
	}

	account, err2 := sc.CalendarAccounts.GetByID(ctx, accountID)
	if err2 != nil {
		t.Fatalf("CalendarAccounts.GetByID: %v", err2)
	}
	if !account.LastSyncError.Valid {
		t.Fatalf("expected the owning account to be marked with the decrypt error too, got %+v", account)
	}
}

// TestSyncOneDecryptErrorAccountIDZero exercises the same decrypt-failure
// path but with a plugin whose CalendarID points at a row that doesn't
// exist, so Calendars.GetByID fails and accountID stays 0 - the
// `if accountID != 0` skip inside the decrypt-error branch.
func TestSyncOneDecryptErrorAccountIDZero(t *testing.T) {
	sc := newSyncContext(t)
	ctx := t.Context()

	p := models.Plugin{
		ID:             "orphan-plugin",
		Name:           "Orphan",
		BaseURL:        "http://example.invalid",
		Enabled:        true,
		EncryptedToken: []byte("not-a-valid-ciphertext"),
	}
	// CalendarID left zero-value (invalid) - GetByID will fail to resolve it.

	err := sc.SyncOne(ctx, p, 7)
	if err == nil {
		t.Fatal("expected a decrypt error")
	}
}

// TestSyncOneFetchEventsErrorWithAccount exercises the FetchEvents-failure
// branch with a real, resolvable owning account (accountID != 0).
func TestSyncOneFetchEventsErrorWithAccount(t *testing.T) {
	sc := newSyncContext(t)
	ctx := t.Context()

	srv := newEventsServer(t, `{}`, http.StatusInternalServerError)
	p, _, accountID := setupPluginAndCalendar(t, sc, srv.URL, "test-token")

	err := sc.SyncOne(ctx, p, 7)
	if err == nil {
		t.Fatal("expected a FetchEvents error")
	}

	account, err2 := sc.CalendarAccounts.GetByID(ctx, accountID)
	if err2 != nil {
		t.Fatalf("CalendarAccounts.GetByID: %v", err2)
	}
	if !account.LastSyncError.Valid {
		t.Fatalf("expected the owning account to be marked with the fetch error, got %+v", account)
	}
}

// TestSyncOneFetchEventsErrorAccountIDZero exercises the FetchEvents-failure
// branch when the calendar can't be resolved to an owning account.
func TestSyncOneFetchEventsErrorAccountIDZero(t *testing.T) {
	sc := newSyncContext(t)
	ctx := t.Context()

	srv := newEventsServer(t, `{}`, http.StatusInternalServerError)
	encToken, err := sc.Encryptor.Encrypt("test-token")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	p := models.Plugin{
		ID:             "orphan-plugin-2",
		Name:           "Orphan 2",
		BaseURL:        srv.URL,
		Enabled:        true,
		EncryptedToken: encToken,
	}

	if err := sc.SyncOne(ctx, p, 7); err == nil {
		t.Fatal("expected a FetchEvents error")
	}
}

// TestSyncOneUpsertErrorContinuesToNextEvent exercises the "continue on
// per-event Upsert failure" branch: the plugin's CalendarID points at a
// calendar row that doesn't exist, so every event's Upsert fails its
// hhq_calendar_events_cache.calendar_id foreign key constraint. SyncOne must
// not abort - it should continue past each failed upsert, still call
// PruneStale (a no-op delete against the same nonexistent calendar_id), and
// return nil (the loop's per-event errors are swallowed by design; only
// PruneStale's own error is ever returned).
func TestSyncOneUpsertErrorContinuesToNextEvent(t *testing.T) {
	sc := newSyncContext(t)
	ctx := t.Context()

	body := `{"events":[
		{"uid":"e1","summary":"One","starts_at":"` + time.Now().Format(time.RFC3339) + `","ends_at":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"},
		{"uid":"e2","summary":"Two","starts_at":"` + time.Now().Format(time.RFC3339) + `","ends_at":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}
	]}`
	srv := newEventsServer(t, body, 0)

	encToken, err := sc.Encryptor.Encrypt("test-token")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	p := models.Plugin{
		ID:             "bad-calendar-plugin",
		Name:           "Bad Calendar",
		BaseURL:        srv.URL,
		Enabled:        true,
		EncryptedToken: encToken,
	}
	p.CalendarID.Int32 = 999999
	p.CalendarID.Valid = true
	if err := sc.Plugins.Create(ctx, p); err != nil {
		t.Fatalf("Plugins.Create: %v", err)
	}
	if err := sc.Plugins.SetToken(ctx, p.ID, encToken); err != nil {
		t.Fatalf("SetToken: %v", err)
	}

	if err := sc.SyncOne(ctx, p, 7); err != nil {
		t.Fatalf("SyncOne: expected nil (per-event upsert errors are swallowed), got %v", err)
	}
}

// TestNullableString covers both branches of the small sync.go helper.
func TestNullableString(t *testing.T) {
	if got := nullableString(""); got.Valid {
		t.Fatalf("nullableString(\"\") = %+v, want Valid=false", got)
	}
	if got := nullableString("x"); !got.Valid || got.String != "x" {
		t.Fatalf("nullableString(\"x\") = %+v", got)
	}
}

// TestConvertActionsEmpty covers convertActions' nil-return branch directly
// (the non-empty branch is already exercised via
// TestSyncOneSuccessUpsertsEventsAndPrunesStale).
func TestConvertActionsEmpty(t *testing.T) {
	if got := convertActions(nil); got != nil {
		t.Fatalf("convertActions(nil) = %+v, want nil", got)
	}
	if got := convertActions([]EventAction{}); got != nil {
		t.Fatalf("convertActions([]) = %+v, want nil", got)
	}
}
