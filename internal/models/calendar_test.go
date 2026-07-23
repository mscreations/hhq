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
	"database/sql"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/testutil"
)

func newTestAccount(t *testing.T, s *CalendarAccountStore, name string) int {
	t.Helper()
	id, err := s.Create(t.Context(), CalendarAccount{
		Name:              name,
		Provider:          ProviderFastmail,
		CalDAVURL:         sql.NullString{String: "https://caldav.fastmail.com/dav/", Valid: true},
		Username:          sql.NullString{String: "user@example.com", Valid: true},
		EncryptedPassword: []byte("encrypted"),
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}
	return id
}

func TestCalendarAccountStoreCreateAndGet(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &CalendarAccountStore{DB: conn}

	id := newTestAccount(t, s, "Fastmail")

	got, err := s.GetByID(t.Context(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Fastmail" || got.Provider != ProviderFastmail {
		t.Fatalf("unexpected account: %+v", got)
	}
}

func TestCalendarAccountStoreGetByIDNotFound(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &CalendarAccountStore{DB: conn}

	if _, err := s.GetByID(t.Context(), 99999); err != ErrAccountNotFound {
		t.Fatalf("GetByID missing: err = %v, want ErrAccountNotFound", err)
	}
}

func TestCalendarAccountStoreGetByName(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &CalendarAccountStore{DB: conn}
	ctx := t.Context()

	id := newTestAccount(t, s, "Fastmail")

	got, err := s.GetByName(ctx, "Fastmail")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.ID != id {
		t.Fatalf("GetByName ID = %d, want %d", got.ID, id)
	}

	if _, err := s.GetByName(ctx, "No Such Account"); err != ErrAccountNotFound {
		t.Fatalf("GetByName missing: err = %v, want ErrAccountNotFound", err)
	}
}

func TestCalendarAccountStoreCreatePersistsBootstrapManaged(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &CalendarAccountStore{DB: conn}
	ctx := t.Context()

	id, err := s.Create(ctx, CalendarAccount{
		Name:              "Bootstrap Fastmail",
		Provider:          ProviderFastmail,
		CalDAVURL:         sql.NullString{String: "https://caldav.fastmail.com/dav/", Valid: true},
		Username:          sql.NullString{String: "user@example.com", Valid: true},
		EncryptedPassword: []byte("encrypted"),
		BootstrapManaged:  true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.BootstrapManaged {
		t.Fatal("expected BootstrapManaged to be true")
	}

	// A plain UI-created account should default to false.
	uiID := newTestAccount(t, s, "UI Fastmail")
	uiAccount, err := s.GetByID(ctx, uiID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if uiAccount.BootstrapManaged {
		t.Fatal("expected BootstrapManaged to default to false")
	}
}

func TestCalendarAccountStoreUpdateBootstrap(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &CalendarAccountStore{DB: conn}
	ctx := t.Context()

	id := newTestAccount(t, s, "Bootstrap Account")

	if err := s.UpdateBootstrap(ctx, id, ProviderICloud, "https://caldav.icloud.com/", "newuser@example.com", []byte("new-encrypted")); err != nil {
		t.Fatalf("UpdateBootstrap: %v", err)
	}

	after, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if after.Provider != ProviderICloud {
		t.Fatalf("Provider = %q, want %q", after.Provider, ProviderICloud)
	}
	if after.CalDAVURL.String != "https://caldav.icloud.com/" {
		t.Fatalf("CalDAVURL = %q, want icloud url", after.CalDAVURL.String)
	}
	if after.Username.String != "newuser@example.com" {
		t.Fatalf("Username = %q, want newuser@example.com", after.Username.String)
	}
	if string(after.EncryptedPassword) != "new-encrypted" {
		t.Fatalf("EncryptedPassword = %q, want new-encrypted", after.EncryptedPassword)
	}
}

func TestDefaultCalDAVURL(t *testing.T) {
	cases := []struct {
		provider    CalendarProvider
		explicitURL string
		want        string
	}{
		{ProviderFastmail, "", "https://caldav.fastmail.com/dav/"},
		{ProviderICloud, "", "https://caldav.icloud.com/"},
		{ProviderFastmail, "https://custom.example.com/dav/", "https://custom.example.com/dav/"},
		{ProviderGeneric, "", ""},
		{ProviderGeneric, "https://caldav.example.com/", "https://caldav.example.com/"},
	}
	for _, c := range cases {
		if got := DefaultCalDAVURL(c.provider, c.explicitURL); got != c.want {
			t.Errorf("DefaultCalDAVURL(%q, %q) = %q, want %q", c.provider, c.explicitURL, got, c.want)
		}
	}
}

func TestCalendarAccountStoreUpdateKeepsPasswordWhenNil(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &CalendarAccountStore{DB: conn}
	ctx := t.Context()

	id := newTestAccount(t, s, "Fastmail")
	before, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if err := s.Update(ctx, id, "Renamed", "https://caldav.fastmail.com/dav/", "user@example.com", nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	after, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if after.Name != "Renamed" {
		t.Fatalf("Name = %q, want %q", after.Name, "Renamed")
	}
	if string(after.EncryptedPassword) != string(before.EncryptedPassword) {
		t.Fatal("expected encrypted password to be unchanged when newEncryptedPassword is nil")
	}
}

func TestCalendarAccountStoreUpdateReplacesPasswordWhenProvided(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &CalendarAccountStore{DB: conn}
	ctx := t.Context()

	id := newTestAccount(t, s, "Fastmail")

	if err := s.Update(ctx, id, "Fastmail", "https://caldav.fastmail.com/dav/", "user@example.com", []byte("new-encrypted")); err != nil {
		t.Fatalf("Update: %v", err)
	}

	after, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if string(after.EncryptedPassword) != "new-encrypted" {
		t.Fatalf("EncryptedPassword = %q, want %q", after.EncryptedPassword, "new-encrypted")
	}
}

func TestCalendarAccountStoreDeleteCascadesToCalendars(t *testing.T) {
	conn := testutil.RequireDB(t)
	accounts := &CalendarAccountStore{DB: conn}
	calendars := &CalendarStore{DB: conn}
	ctx := t.Context()

	accountID := newTestAccount(t, accounts, "Fastmail")
	calID, err := calendars.UpsertDiscovered(ctx, accountID, "/dav/calendars/user/home/", "Home", "#3B82F6")
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}

	if err := accounts.Delete(ctx, accountID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := calendars.GetByID(ctx, calID); err != ErrNotFound {
		t.Fatalf("expected calendar to be cascade-deleted with its account, got err=%v", err)
	}
}

func TestCalendarAccountStoreMarkSynced(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &CalendarAccountStore{DB: conn}
	ctx := t.Context()

	id := newTestAccount(t, s, "Fastmail")

	if err := s.MarkSynced(ctx, id, nil); err != nil {
		t.Fatalf("MarkSynced(nil): %v", err)
	}
	got, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.LastSyncedAt.Valid || got.LastSyncError.Valid {
		t.Fatalf("expected synced timestamp set and no error, got %+v", got)
	}

	if err := s.MarkSynced(ctx, id, sql.ErrConnDone); err != nil {
		t.Fatalf("MarkSynced(err): %v", err)
	}
	got, err = s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.LastSyncError.Valid || got.LastSyncError.String != sql.ErrConnDone.Error() {
		t.Fatalf("expected sync error recorded, got %+v", got.LastSyncError)
	}
}

func TestCalendarStoreUpsertDiscoveredInsertsThenUpdatesNameOnly(t *testing.T) {
	conn := testutil.RequireDB(t)
	accounts := &CalendarAccountStore{DB: conn}
	calendars := &CalendarStore{DB: conn}
	ctx := t.Context()

	accountID := newTestAccount(t, accounts, "Fastmail")

	id1, err := calendars.UpsertDiscovered(ctx, accountID, "/dav/calendars/user/home/", "Home", "#3B82F6")
	if err != nil {
		t.Fatalf("UpsertDiscovered (insert): %v", err)
	}
	if err := calendars.SetEnabled(ctx, id1, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if err := calendars.SetColor(ctx, id1, "#EF4444"); err != nil {
		t.Fatalf("SetColor: %v", err)
	}

	// Re-discovering the same (account, external_path) with a new name and a
	// different color argument must only update the name — color and enabled
	// are the parent's own choices and must survive re-discovery untouched.
	id2, err := calendars.UpsertDiscovered(ctx, accountID, "/dav/calendars/user/home/", "Home (renamed on server)", "#22C55E")
	if err != nil {
		t.Fatalf("UpsertDiscovered (conflict): %v", err)
	}
	if id2 != id1 {
		t.Fatalf("expected UpsertDiscovered to return the same id on conflict: got %d, want %d", id2, id1)
	}

	got, err := calendars.GetByID(ctx, id1)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Home (renamed on server)" {
		t.Fatalf("Name = %q, want the refreshed name", got.Name)
	}
	if got.Color != "#EF4444" {
		t.Fatalf("Color = %q, want the parent's chosen color to survive re-discovery", got.Color)
	}
	if got.Enabled {
		t.Fatal("expected Enabled to remain false (parent's choice) after re-discovery")
	}
}

func TestCalendarStoreNextAvailableColorSkipsUsedColors(t *testing.T) {
	conn := testutil.RequireDB(t)
	accounts := &CalendarAccountStore{DB: conn}
	calendars := &CalendarStore{DB: conn}
	ctx := t.Context()

	accountID := newTestAccount(t, accounts, "Fastmail")

	first, err := calendars.NextAvailableColor(ctx)
	if err != nil {
		t.Fatalf("NextAvailableColor: %v", err)
	}
	if _, err := calendars.UpsertDiscovered(ctx, accountID, "/path/a/", "A", first); err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}

	second, err := calendars.NextAvailableColor(ctx)
	if err != nil {
		t.Fatalf("NextAvailableColor: %v", err)
	}
	if second == first {
		t.Fatalf("expected a different color once %q is in use", first)
	}
}

func TestCalendarStoreListEnabledForAccountFiltersDisabled(t *testing.T) {
	conn := testutil.RequireDB(t)
	accounts := &CalendarAccountStore{DB: conn}
	calendars := &CalendarStore{DB: conn}
	ctx := t.Context()

	accountID := newTestAccount(t, accounts, "Fastmail")
	enabledID, err := calendars.UpsertDiscovered(ctx, accountID, "/path/home/", "Home", "#3B82F6")
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}
	disabledID, err := calendars.UpsertDiscovered(ctx, accountID, "/path/work/", "Work", "#EF4444")
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}
	if err := calendars.SetEnabled(ctx, disabledID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	list, err := calendars.ListEnabledForAccount(ctx, accountID)
	if err != nil {
		t.Fatalf("ListEnabledForAccount: %v", err)
	}
	if len(list) != 1 || list[0].ID != enabledID {
		t.Fatalf("ListEnabledForAccount = %+v, want only the enabled calendar (id %d)", list, enabledID)
	}
}

func TestCalendarStoreListForAccount(t *testing.T) {
	conn := testutil.RequireDB(t)
	accounts := &CalendarAccountStore{DB: conn}
	calendars := &CalendarStore{DB: conn}
	ctx := t.Context()

	accountID := newTestAccount(t, accounts, "Fastmail")
	otherAccountID := newTestAccount(t, accounts, "iCloud")

	if _, err := calendars.UpsertDiscovered(ctx, accountID, "/path/home/", "Home", "#3B82F6"); err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}
	if _, err := calendars.UpsertDiscovered(ctx, accountID, "/path/work/", "Work", "#EF4444"); err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}
	if _, err := calendars.UpsertDiscovered(ctx, otherAccountID, "/path/other/", "Other", "#22C55E"); err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}

	list, err := calendars.ListForAccount(ctx, accountID)
	if err != nil {
		t.Fatalf("ListForAccount: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListForAccount = %+v, want 2 calendars scoped to the account", list)
	}
	for _, c := range list {
		if c.AccountName != "Fastmail" {
			t.Errorf("AccountName = %q, want %q", c.AccountName, "Fastmail")
		}
	}
}

func TestCalendarStoreListAllWithAccount(t *testing.T) {
	conn := testutil.RequireDB(t)
	accounts := &CalendarAccountStore{DB: conn}
	calendars := &CalendarStore{DB: conn}
	ctx := t.Context()

	accountID := newTestAccount(t, accounts, "Fastmail")
	if _, err := calendars.UpsertDiscovered(ctx, accountID, "/path/home/", "Home", "#3B82F6"); err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}

	list, err := calendars.ListAllWithAccount(ctx)
	if err != nil {
		t.Fatalf("ListAllWithAccount: %v", err)
	}
	if len(list) != 1 || list[0].AccountName != "Fastmail" {
		t.Fatalf("ListAllWithAccount = %+v, want the joined account name", list)
	}
}

// --- EventStore ---

func setupCalendarForEvents(t *testing.T, conn *sql.DB) *Calendar {
	t.Helper()
	accounts := &CalendarAccountStore{DB: conn}
	calendars := &CalendarStore{DB: conn}
	ctx := t.Context()

	accountID := newTestAccount(t, accounts, "Fastmail")
	calID, err := calendars.UpsertDiscovered(ctx, accountID, "/path/home/", "Home", "#3B82F6")
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}
	cal, err := calendars.GetByID(ctx, calID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	return cal
}

func TestEventStoreUpsertInsertsAndUpdates(t *testing.T) {
	conn := testutil.RequireDB(t)
	cal := setupCalendarForEvents(t, conn)
	events := &EventStore{DB: conn}
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	e := Event{
		CalendarID: cal.ID,
		UID:        "event-1@example.com",
		Summary:    "Team meeting",
		StartsAt:   now,
		EndsAt:     now.Add(time.Hour),
	}
	if err := events.Upsert(ctx, e); err != nil {
		t.Fatalf("Upsert (insert): %v", err)
	}

	e.Summary = "Team meeting (rescheduled)"
	if err := events.Upsert(ctx, e); err != nil {
		t.Fatalf("Upsert (update): %v", err)
	}

	today, err := events.ListToday(ctx)
	if err != nil {
		t.Fatalf("ListToday: %v", err)
	}
	if len(today) != 1 {
		t.Fatalf("expected upsert to produce exactly one row, got %d", len(today))
	}
	if today[0].Summary != "Team meeting (rescheduled)" {
		t.Fatalf("Summary = %q, want the updated summary", today[0].Summary)
	}
}

func TestEventStorePruneStaleRemovesUnseenUIDsOnly(t *testing.T) {
	conn := testutil.RequireDB(t)
	cal := setupCalendarForEvents(t, conn)
	events := &EventStore{DB: conn}
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	for _, uid := range []string{"keep-1", "remove-1", "remove-2"} {
		if err := events.Upsert(ctx, Event{
			CalendarID: cal.ID,
			UID:        uid,
			Summary:    uid,
			StartsAt:   now,
			EndsAt:     now.Add(time.Hour),
		}); err != nil {
			t.Fatalf("Upsert(%s): %v", uid, err)
		}
	}

	if err := events.PruneStale(ctx, cal.ID, []string{"keep-1"}); err != nil {
		t.Fatalf("PruneStale: %v", err)
	}

	remaining, err := events.ListToday(ctx)
	if err != nil {
		t.Fatalf("ListToday: %v", err)
	}
	if len(remaining) != 1 || remaining[0].UID != "keep-1" {
		t.Fatalf("remaining = %+v, want only keep-1", remaining)
	}
}

func TestEventStorePruneStaleWithEmptySeenListRemovesAll(t *testing.T) {
	conn := testutil.RequireDB(t)
	cal := setupCalendarForEvents(t, conn)
	events := &EventStore{DB: conn}
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	if err := events.Upsert(ctx, Event{
		CalendarID: cal.ID, UID: "solo", Summary: "solo", StartsAt: now, EndsAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := events.PruneStale(ctx, cal.ID, []string{}); err != nil {
		t.Fatalf("PruneStale with empty seen list: %v", err)
	}

	remaining, err := events.ListToday(ctx)
	if err != nil {
		t.Fatalf("ListToday: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected all events removed when seenUIDs is empty, got %+v", remaining)
	}
}

// TestEventStoreListInWindow is a direct regression test for CLAUDE.md round
// 3's bug: ListInWindow's SQL used `($1 || ' days')::interval`, which fails
// with a Postgres type-inference error since `||` isn't defined for
// (integer, text). The fix was `make_interval(days => $1)`. This test
// exercises the real query against real Postgres so a regression would
// surface as a query error, not just a logic error.
func TestEventStoreListInWindow(t *testing.T) {
	conn := testutil.RequireDB(t)
	cal := setupCalendarForEvents(t, conn)
	events := &EventStore{DB: conn}
	ctx := t.Context()

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	cases := []struct {
		uid    string
		offset time.Duration
	}{
		{"already-passed-today", 1 * time.Hour}, // still "today", even though likely in the past relative to now
		{"in-3-days", 3 * 24 * time.Hour},
		{"in-10-days", 10 * 24 * time.Hour}, // must be excluded by a 7-day window
	}
	for _, c := range cases {
		if err := events.Upsert(ctx, Event{
			CalendarID: cal.ID,
			UID:        c.uid,
			Summary:    c.uid,
			StartsAt:   today.Add(c.offset),
			EndsAt:     today.Add(c.offset + time.Hour),
		}); err != nil {
			t.Fatalf("Upsert(%s): %v", c.uid, err)
		}
	}

	got, err := events.ListInWindow(ctx, 7)
	if err != nil {
		t.Fatalf("ListInWindow: %v", err)
	}

	uids := map[string]bool{}
	for _, e := range got {
		uids[e.UID] = true
	}
	if !uids["already-passed-today"] {
		t.Error("expected today's event to be included even though its start time may already have passed")
	}
	if !uids["in-3-days"] {
		t.Error("expected an event 3 days out to be included within a 7-day window")
	}
	if uids["in-10-days"] {
		t.Error("expected an event 10 days out to be excluded from a 7-day window")
	}
}

func TestEventStoreListInWindowExcludesDisabledCalendars(t *testing.T) {
	conn := testutil.RequireDB(t)
	cal := setupCalendarForEvents(t, conn)
	calendars := &CalendarStore{DB: conn}
	events := &EventStore{DB: conn}
	ctx := t.Context()

	now := time.Now()
	if err := events.Upsert(ctx, Event{
		CalendarID: cal.ID, UID: "e1", Summary: "e1", StartsAt: now, EndsAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := calendars.SetEnabled(ctx, cal.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	got, err := events.ListInWindow(ctx, 7)
	if err != nil {
		t.Fatalf("ListInWindow: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected disabling the calendar to immediately hide its events, got %+v", got)
	}
}

func TestEventStoreListTodayExcludesTomorrow(t *testing.T) {
	conn := testutil.RequireDB(t)
	cal := setupCalendarForEvents(t, conn)
	events := &EventStore{DB: conn}
	ctx := t.Context()

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if err := events.Upsert(ctx, Event{
		CalendarID: cal.ID, UID: "today-event", Summary: "today", StartsAt: today.Add(time.Hour), EndsAt: today.Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := events.Upsert(ctx, Event{
		CalendarID: cal.ID, UID: "tomorrow-event", Summary: "tomorrow", StartsAt: today.Add(25 * time.Hour), EndsAt: today.Add(26 * time.Hour),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := events.ListToday(ctx)
	if err != nil {
		t.Fatalf("ListToday: %v", err)
	}
	if len(got) != 1 || got[0].UID != "today-event" {
		t.Fatalf("ListToday = %+v, want only today's event", got)
	}
}

// TestEventStoreGetByIDRoundTripsAttendeesAndAttachments exercises the
// marshal/unmarshal helpers for Attendees/Attachments indirectly (they're
// unexported), by writing an event with both populated and reading it back
// via GetByID - the only place that decodes them.
func TestEventStoreGetByIDRoundTripsAttendeesAndAttachments(t *testing.T) {
	conn := testutil.RequireDB(t)
	cal := setupCalendarForEvents(t, conn)
	events := &EventStore{DB: conn}
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	e := Event{
		CalendarID:     cal.ID,
		UID:            "detail-event@example.com",
		Summary:        "Team meeting",
		Location:       sql.NullString{String: "Kitchen Table", Valid: true},
		Description:    sql.NullString{String: "Weekly sync", Valid: true},
		OrganizerName:  sql.NullString{String: "Alice", Valid: true},
		OrganizerEmail: sql.NullString{String: "alice@example.com", Valid: true},
		Attendees: []Attendee{
			{Name: "Bob", Email: "bob@example.com", Status: "ACCEPTED"},
			{Name: "Carol", Email: "carol@example.com", Status: "NEEDS-ACTION"},
		},
		Attachments: []Attachment{
			{Name: "Agenda", URI: "https://example.com/agenda.pdf"},
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
	if len(got.Attendees) != 2 || got.Attendees[0].Email != "bob@example.com" || got.Attendees[1].Status != "NEEDS-ACTION" {
		t.Fatalf("Attendees = %+v, want the two seeded attendees round-tripped", got.Attendees)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].URI != "https://example.com/agenda.pdf" {
		t.Fatalf("Attachments = %+v, want the seeded attachment round-tripped", got.Attachments)
	}
	if got.Description.String != "Weekly sync" || got.OrganizerName.String != "Alice" {
		t.Fatalf("detail fields = %+v, want description/organizer populated", got)
	}
}

// TestEventStoreGetByIDWithNoAttendeesOrAttachments exercises the NULL-string
// short-circuit path in marshalAttendees/marshalAttachments (empty lists
// stored as NULL rather than "[]") and the matching unmarshal path.
func TestEventStoreGetByIDWithNoAttendeesOrAttachments(t *testing.T) {
	conn := testutil.RequireDB(t)
	cal := setupCalendarForEvents(t, conn)
	events := &EventStore{DB: conn}
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	if err := events.Upsert(ctx, Event{
		CalendarID: cal.ID, UID: "bare-event", Summary: "Bare", StartsAt: now, EndsAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	list, err := events.ListToday(ctx)
	if err != nil {
		t.Fatalf("ListToday: %v", err)
	}

	got, err := events.GetByID(ctx, list[0].ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(got.Attendees) != 0 || len(got.Attachments) != 0 {
		t.Fatalf("expected no attendees/attachments, got %+v / %+v", got.Attendees, got.Attachments)
	}
}

func TestEventStoreGetByIDNotFound(t *testing.T) {
	conn := testutil.RequireDB(t)
	events := &EventStore{DB: conn}

	if _, err := events.GetByID(t.Context(), 99999); err != ErrNotFound {
		t.Fatalf("GetByID missing: err = %v, want ErrNotFound", err)
	}
}
