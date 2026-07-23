package models

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/testutil"
)

// TestCalendarAccountStoreListAll covers ListAll and, transitively,
// scanCalendarAccounts' loop body (the non-error path - see
// TestListQueriesReturnErrorOnCanceledContext for the error path).
func TestCalendarAccountStoreListAll(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &CalendarAccountStore{DB: conn}
	ctx := t.Context()

	newTestAccount(t, s, "Zebra")
	newTestAccount(t, s, "Alpha")

	list, err := s.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(list) != 2 || list[0].Name != "Alpha" || list[1].Name != "Zebra" {
		t.Fatalf("ListAll = %+v, want Alpha then Zebra (ORDER BY name)", list)
	}
}

// TestCalendarAccountStoreGoogleFlow covers CreateGoogle, UpdateGoogleToken,
// and UpdateGoogleName end to end (none previously exercised by any test).
func TestCalendarAccountStoreGoogleFlow(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &CalendarAccountStore{DB: conn}
	ctx := t.Context()

	id, err := s.CreateGoogle(ctx, "My Google Calendar", []byte("encrypted-refresh-token"), "user@gmail.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}

	got, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Provider != ProviderGoogle || string(got.EncryptedRefreshToken) != "encrypted-refresh-token" || got.GoogleEmail.String != "user@gmail.com" {
		t.Fatalf("unexpected google account: %+v", got)
	}

	if err := s.UpdateGoogleToken(ctx, id, []byte("new-refresh-token"), "newuser@gmail.com"); err != nil {
		t.Fatalf("UpdateGoogleToken: %v", err)
	}
	afterToken, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if string(afterToken.EncryptedRefreshToken) != "new-refresh-token" || afterToken.GoogleEmail.String != "newuser@gmail.com" {
		t.Fatalf("UpdateGoogleToken did not apply: %+v", afterToken)
	}

	if err := s.UpdateGoogleName(ctx, id, "Renamed Google Calendar"); err != nil {
		t.Fatalf("UpdateGoogleName: %v", err)
	}
	afterName, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if afterName.Name != "Renamed Google Calendar" {
		t.Fatalf("UpdateGoogleName did not apply: %+v", afterName)
	}
	// UpdateGoogleName must not disturb the token/email set by UpdateGoogleToken.
	if string(afterName.EncryptedRefreshToken) != "new-refresh-token" {
		t.Fatalf("UpdateGoogleName unexpectedly touched the refresh token: %+v", afterName)
	}
}

// TestPaletteColorsReturnsIndependentCopy covers PaletteColors and asserts
// that mutating the returned slice can't corrupt the package-level palette
// used for real color assignment.
func TestPaletteColorsReturnsIndependentCopy(t *testing.T) {
	got := PaletteColors()
	if len(got) != len(colorPalette) {
		t.Fatalf("PaletteColors() len = %d, want %d", len(got), len(colorPalette))
	}
	got[0] = "#000000"
	if colorPalette[0] == "#000000" {
		t.Fatal("mutating the returned slice corrupted the package-level colorPalette")
	}
}

// TestCalendarStoreNextAvailableColorCyclesOncePaletteExhausted covers the
// modulo-cycling fallback in NextAvailableColor - reached only once every
// palette color is already assigned to some calendar.
func TestCalendarStoreNextAvailableColorCyclesOncePaletteExhausted(t *testing.T) {
	conn := testutil.RequireDB(t)
	accounts := &CalendarAccountStore{DB: conn}
	calendars := &CalendarStore{DB: conn}
	ctx := t.Context()

	accountID := newTestAccount(t, accounts, "Fastmail")
	for i, c := range colorPalette {
		path := "/path/" + c + "/"
		if _, err := calendars.UpsertDiscovered(ctx, accountID, path, "Cal", c); err != nil {
			t.Fatalf("UpsertDiscovered(%d): %v", i, err)
		}
	}

	got, err := calendars.NextAvailableColor(ctx)
	if err != nil {
		t.Fatalf("NextAvailableColor: %v", err)
	}
	// total (len(colorPalette)) % len(colorPalette) == 0 - cycles back to the
	// first palette color.
	if got != colorPalette[0] {
		t.Fatalf("NextAvailableColor once exhausted = %q, want cycled color %q", got, colorPalette[0])
	}
}

// TestCalendarStoreMarkSynced covers the per-calendar MarkSynced (distinct
// from CalendarAccountStore.MarkSynced, which already has coverage).
func TestCalendarStoreMarkSynced(t *testing.T) {
	conn := testutil.RequireDB(t)
	accounts := &CalendarAccountStore{DB: conn}
	calendars := &CalendarStore{DB: conn}
	ctx := t.Context()

	accountID := newTestAccount(t, accounts, "Fastmail")
	calID, err := calendars.UpsertDiscovered(ctx, accountID, "/path/home/", "Home", "#3B82F6")
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}

	if err := calendars.MarkSynced(ctx, calID, nil); err != nil {
		t.Fatalf("MarkSynced(nil): %v", err)
	}
	got, err := calendars.GetByID(ctx, calID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.LastSyncedAt.Valid || got.LastSyncError.Valid {
		t.Fatalf("expected synced timestamp set and no error, got %+v", got)
	}

	if err := calendars.MarkSynced(ctx, calID, sql.ErrConnDone); err != nil {
		t.Fatalf("MarkSynced(err): %v", err)
	}
	got, err = calendars.GetByID(ctx, calID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.LastSyncError.Valid || got.LastSyncError.String != sql.ErrConnDone.Error() {
		t.Fatalf("expected sync error recorded, got %+v", got.LastSyncError)
	}
}

// TestEventStoreUpsertWithActionsRoundTrips exercises marshalActions'
// non-empty encode path and unmarshalActions' non-empty decode path (both
// were only exercised on their empty/NULL short-circuit branch before).
func TestEventStoreUpsertWithActionsRoundTrips(t *testing.T) {
	conn := testutil.RequireDB(t)
	cal := setupCalendarForEvents(t, conn)
	events := &EventStore{DB: conn}
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	e := Event{
		CalendarID: cal.ID,
		UID:        "actions-event@example.com",
		Summary:    "Bill due",
		Actions: []EventAction{
			{ID: "mark-paid", Label: "Mark Paid", RequiresParent: true},
			{ID: "snooze", Label: "Snooze"},
		},
		StartsAt: now,
		EndsAt:   now.Add(time.Hour),
	}
	if err := events.Upsert(ctx, e); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	list, err := events.ListToday(ctx)
	if err != nil {
		t.Fatalf("ListToday: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly one event, got %d", len(list))
	}

	got, err := events.GetByID(ctx, list[0].ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(got.Actions) != 2 || got.Actions[0].ID != "mark-paid" || !got.Actions[0].RequiresParent || got.Actions[1].Label != "Snooze" {
		t.Fatalf("Actions = %+v, want the two seeded actions round-tripped", got.Actions)
	}
}

// TestEventStoreGetByIDUnmarshalErrors is a direct test of GetByID's three
// unmarshal error branches (unmarshalAttendees/unmarshalAttachments/
// unmarshalActions), by writing malformed JSON straight into the relevant
// column via raw SQL (bypassing Upsert, which never itself produces invalid
// JSON) and confirming GetByID surfaces the json error rather than silently
// dropping the field.
func TestEventStoreGetByIDUnmarshalErrors(t *testing.T) {
	conn := testutil.RequireDB(t)
	cal := setupCalendarForEvents(t, conn)
	events := &EventStore{DB: conn}
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)

	cases := []struct {
		name   string
		column string
	}{
		{"attendees", "attendees"},
		{"attachments", "attachments"},
		{"actions", "actions"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			uid := "bad-json-" + c.name
			if err := events.Upsert(ctx, Event{
				CalendarID: cal.ID, UID: uid, Summary: uid, StartsAt: now, EndsAt: now.Add(time.Hour),
			}); err != nil {
				t.Fatalf("Upsert: %v", err)
			}

			var id int
			if err := conn.QueryRowContext(ctx, `SELECT id FROM hhq_calendar_events_cache WHERE uid = $1`, uid).Scan(&id); err != nil {
				t.Fatalf("looking up inserted event id: %v", err)
			}

			// #nosec G201 - column name comes from the fixed cases table above, not user input.
			if _, err := conn.ExecContext(ctx, `UPDATE hhq_calendar_events_cache SET `+c.column+` = 'not valid json' WHERE id = $1`, id); err != nil {
				t.Fatalf("corrupting %s column: %v", c.column, err)
			}

			if _, err := events.GetByID(ctx, id); err == nil {
				t.Fatalf("expected GetByID to surface a json unmarshal error for a malformed %s column", c.column)
			}
		})
	}
}

// TestListQueriesReturnErrorOnCanceledContext is a direct test of the
// query-error return branch shared by every List*/GetByID method in this
// package that wraps a *sql.Rows/QueryRowContext call - the branch is
// otherwise unreachable without actually breaking the DB connection. A
// context canceled before the call reaches the driver reliably triggers the
// same "err != nil { return ... }" path.
func TestListQueriesReturnErrorOnCanceledContext(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	accounts := &CalendarAccountStore{DB: conn}
	calendars := &CalendarStore{DB: conn}
	events := &EventStore{DB: conn}

	if _, err := accounts.ListAll(ctx); err == nil {
		t.Error("CalendarAccountStore.ListAll: expected error on canceled context")
	}
	if _, err := calendars.ListForAccount(ctx, 1); err == nil {
		t.Error("CalendarStore.ListForAccount: expected error on canceled context")
	}
	if _, err := calendars.ListAllWithAccount(ctx); err == nil {
		t.Error("CalendarStore.ListAllWithAccount: expected error on canceled context")
	}
	if _, err := calendars.ListEnabledForAccount(ctx, 1); err == nil {
		t.Error("CalendarStore.ListEnabledForAccount: expected error on canceled context")
	}
	if _, err := events.ListInWindow(ctx, 7); err == nil {
		t.Error("EventStore.ListInWindow: expected error on canceled context")
	}
	if _, err := events.ListToday(ctx); err == nil {
		t.Error("EventStore.ListToday: expected error on canceled context")
	}
	if _, err := events.GetByID(ctx, 1); err == nil {
		t.Error("EventStore.GetByID: expected error on canceled context")
	}
}
