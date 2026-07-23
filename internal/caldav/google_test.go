package caldav

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	googleapi "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
)

// --- flattenGoogleEvent / parseGoogleEventDateTime: pure, no network ---

func TestFlattenGoogleEventExtractsBasicFields(t *testing.T) {
	ev := &googleapi.Event{
		Id:       "abc123",
		ICalUID:  "event-1@google.com",
		Summary:  "Team meeting",
		Location: "Kitchen Table",
		Start:    &googleapi.EventDateTime{DateTime: "2026-08-01T10:00:00-04:00"},
		End:      &googleapi.EventDateTime{DateTime: "2026-08-01T11:00:00-04:00"},
		Organizer: &googleapi.EventOrganizer{
			DisplayName: "Jon",
			Email:       "jon@example.com",
		},
	}

	fe, ok := flattenGoogleEvent(ev)
	if !ok {
		t.Fatal("expected flattenGoogleEvent to accept a normal timed event")
	}
	if fe.UID != "event-1@google.com" || fe.Summary != "Team meeting" || fe.Location != "Kitchen Table" {
		t.Fatalf("unexpected event: %+v", fe)
	}
	if fe.AllDay {
		t.Error("expected a DateTime-based event to not be marked all-day")
	}
	if fe.OrganizerName != "Jon" || fe.OrganizerEmail != "jon@example.com" {
		t.Fatalf("unexpected organizer: name=%q email=%q", fe.OrganizerName, fe.OrganizerEmail)
	}
}

// TestFlattenGoogleEventFallsBackToIdWhenICalUIDEmpty covers Google events
// that lack an iCalUID (some synthetic Google-generated events do) - the UID
// must fall back to the event's own Id so it still gets a stable dedup key
// for EventStore.Upsert/PruneStale.
func TestFlattenGoogleEventFallsBackToIdWhenICalUIDEmpty(t *testing.T) {
	ev := &googleapi.Event{
		Id:    "raw-id-only",
		Start: &googleapi.EventDateTime{DateTime: "2026-08-01T10:00:00Z"},
	}
	fe, ok := flattenGoogleEvent(ev)
	if !ok || fe.UID != "raw-id-only" {
		t.Fatalf("flattenGoogleEvent = %+v, ok=%v, want UID to fall back to Id", fe, ok)
	}
}

func TestFlattenGoogleEventSkipsEventsWithNoUsableID(t *testing.T) {
	ev := &googleapi.Event{Start: &googleapi.EventDateTime{DateTime: "2026-08-01T10:00:00Z"}}
	if _, ok := flattenGoogleEvent(ev); ok {
		t.Fatal("expected an event with neither ICalUID nor Id to be dropped")
	}
}

func TestFlattenGoogleEventSkipsCancelledEvents(t *testing.T) {
	ev := &googleapi.Event{
		Id:     "cancelled-1",
		Status: "cancelled",
		Start:  &googleapi.EventDateTime{DateTime: "2026-08-01T10:00:00Z"},
	}
	if _, ok := flattenGoogleEvent(ev); ok {
		t.Fatal("expected a cancelled event to be dropped")
	}
}

// TestFlattenGoogleEventHidesDeclinedEvents is a direct regression test for
// the confirmed product decision (see plan discussion) to hide events the
// connecting account has declined, rather than showing them on the kiosk.
func TestFlattenGoogleEventHidesDeclinedEvents(t *testing.T) {
	ev := &googleapi.Event{
		Id:    "declined-1",
		Start: &googleapi.EventDateTime{DateTime: "2026-08-01T10:00:00Z"},
		Attendees: []*googleapi.EventAttendee{
			{Email: "someone-else@example.com", ResponseStatus: "accepted"},
			{Self: true, Email: "me@example.com", ResponseStatus: "declined"},
		},
	}
	if _, ok := flattenGoogleEvent(ev); ok {
		t.Fatal("expected an event declined by the connecting account (Self=true) to be dropped")
	}
}

// TestFlattenGoogleEventKeepsEventsDeclinedByOtherAttendees ensures the
// declined-filter only looks at the connecting account's own RSVP (Self),
// not any other attendee's - an event shouldn't disappear from the kiosk
// just because someone else said no.
func TestFlattenGoogleEventKeepsEventsDeclinedByOtherAttendees(t *testing.T) {
	ev := &googleapi.Event{
		Id:    "other-declined-1",
		Start: &googleapi.EventDateTime{DateTime: "2026-08-01T10:00:00Z"},
		Attendees: []*googleapi.EventAttendee{
			{Self: true, Email: "me@example.com", ResponseStatus: "accepted"},
			{Email: "someone-else@example.com", ResponseStatus: "declined"},
		},
	}
	if _, ok := flattenGoogleEvent(ev); !ok {
		t.Fatal("expected an event only declined by a non-self attendee to be kept")
	}
}

func TestFlattenGoogleEventAllDayNormalizesToLocalMidnight(t *testing.T) {
	ev := &googleapi.Event{
		Id:    "allday-1",
		Start: &googleapi.EventDateTime{Date: "2026-08-01"},
		End:   &googleapi.EventDateTime{Date: "2026-08-02"},
	}
	fe, ok := flattenGoogleEvent(ev)
	if !ok {
		t.Fatal("expected an all-day event to be accepted")
	}
	if !fe.AllDay {
		t.Error("expected AllDay to be true for a Date-only event")
	}
	want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local)
	if !fe.StartsAt.Equal(want) {
		t.Fatalf("StartsAt = %v, want local midnight %v", fe.StartsAt, want)
	}
}

func TestFlattenGoogleEventMissingEndFallsBackToStartPlusHour(t *testing.T) {
	ev := &googleapi.Event{
		Id:    "no-end-1",
		Start: &googleapi.EventDateTime{DateTime: "2026-08-01T10:00:00Z"},
	}
	fe, ok := flattenGoogleEvent(ev)
	if !ok {
		t.Fatal("expected event to be accepted")
	}
	if !fe.EndsAt.Equal(fe.StartsAt.Add(time.Hour)) {
		t.Fatalf("EndsAt = %v, want StartsAt+1h = %v", fe.EndsAt, fe.StartsAt.Add(time.Hour))
	}
}

// --- end-to-end: fake Google token + Calendar API server, real Postgres ---

// newFakeGoogleServer builds a single httptest server that answers both the
// OAuth2 token endpoint and the Calendar API's CalendarList/Events endpoints,
// so SyncGoogleAccount can be exercised without ever contacting the real
// Google servers - mirroring the fake-server approach caldav_test.go uses for
// CalDAV's equivalent end-to-end test.
func newFakeGoogleServer(t *testing.T, calendars []*googleapi.CalendarListEntry, eventsByCalendar map[string][]*googleapi.Event) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})

	mux.HandleFunc("/users/me/calendarList", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&googleapi.CalendarList{Items: calendars})
	})

	mux.HandleFunc("/calendars/", func(w http.ResponseWriter, r *http.Request) {
		// Path shape: /calendars/{calendarId}/events
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/calendars/"), "/events")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&googleapi.Events{Items: eventsByCalendar[id]})
	})

	return httptest.NewServer(mux)
}

func fakeGoogleOAuthConfig(srv *httptest.Server) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     "fake-client-id",
		ClientSecret: "fake-client-secret",
		Endpoint:     oauth2.Endpoint{TokenURL: srv.URL + "/token"},
	}
}

// syncGoogleAccountAgainstFakeServer is a small test-only wrapper duplicating
// SyncGoogleAccount's client construction so the Calendar API client can be
// pointed at the fake server via option.WithEndpoint - SyncGoogleAccount
// itself always talks to the real Google API host, so tests build the same
// client shape manually.
func syncGoogleAccountAgainstFakeServer(ctx context.Context, calendars *models.CalendarStore, events *models.EventStore, account models.CalendarAccount, oauthCfg *oauth2.Config, refreshToken string, srv *httptest.Server, windowDays int) error {
	tokenSource := oauthCfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	svc, err := googleapi.NewService(ctx, option.WithTokenSource(tokenSource), option.WithEndpoint(srv.URL))
	if err != nil {
		return err
	}
	discovered, err := googleDiscoverAndUpsert(ctx, calendars, svc, account)
	if err != nil {
		return err
	}
	now := time.Now()
	windowStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	windowEnd := windowStart.AddDate(0, 0, windowDays)
	for _, cal := range discovered {
		if !cal.Enabled {
			continue
		}
		googleSyncCalendarEvents(ctx, calendars, events, cal, svc, windowStart, windowEnd)
	}
	return nil
}

func TestSyncGoogleAccountEndToEnd(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	srv := newFakeGoogleServer(t,
		[]*googleapi.CalendarListEntry{{Id: "primary", Summary: "Home"}},
		map[string][]*googleapi.Event{
			"primary": {
				{
					Id:      "raw-1",
					ICalUID: "test-event-1@google.com",
					Summary: "Fake Google Event",
					Start:   &googleapi.EventDateTime{DateTime: time.Now().Format(time.RFC3339)},
					End:     &googleapi.EventDateTime{DateTime: time.Now().Add(time.Hour).Format(time.RFC3339)},
				},
			},
		})
	defer srv.Close()

	accounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}

	accountID, err := accounts.CreateGoogle(ctx, "Test Google Account", []byte("unused-in-this-test"), "me@example.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}
	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	oauthCfg := fakeGoogleOAuthConfig(srv)
	if err := syncGoogleAccountAgainstFakeServer(ctx, calendars, events, *account, oauthCfg, "fake-refresh-token", srv, 7); err != nil {
		t.Fatalf("sync: %v", err)
	}

	cals, err := calendars.ListAllWithAccount(ctx)
	if err != nil {
		t.Fatalf("ListAllWithAccount: %v", err)
	}
	if len(cals) != 1 || cals[0].Name != "Home" {
		t.Fatalf("discovered calendars = %+v, want a single 'Home' calendar", cals)
	}

	today, err := events.ListToday(ctx)
	if err != nil {
		t.Fatalf("ListToday: %v", err)
	}
	if len(today) != 1 || today[0].UID != "test-event-1@google.com" {
		t.Fatalf("synced events = %+v, want the single fake event", today)
	}
}

// TestSyncGoogleAccountFiltersDeclinedEventEndToEnd confirms the
// declined-event filter (unit-tested above on flattenGoogleEvent directly)
// also holds through the full discovery+sync path into the real cache table.
func TestSyncGoogleAccountFiltersDeclinedEventEndToEnd(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	srv := newFakeGoogleServer(t,
		[]*googleapi.CalendarListEntry{{Id: "primary", Summary: "Home"}},
		map[string][]*googleapi.Event{
			"primary": {
				{
					Id:      "declined-raw",
					ICalUID: "declined@google.com",
					Summary: "Declined Event",
					Start:   &googleapi.EventDateTime{DateTime: time.Now().Format(time.RFC3339)},
					End:     &googleapi.EventDateTime{DateTime: time.Now().Add(time.Hour).Format(time.RFC3339)},
					Attendees: []*googleapi.EventAttendee{
						{Self: true, ResponseStatus: "declined"},
					},
				},
			},
		})
	defer srv.Close()

	accounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}

	accountID, err := accounts.CreateGoogle(ctx, "Test Google Account", []byte("unused"), "me@example.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}
	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	oauthCfg := fakeGoogleOAuthConfig(srv)
	if err := syncGoogleAccountAgainstFakeServer(ctx, calendars, events, *account, oauthCfg, "fake-refresh-token", srv, 7); err != nil {
		t.Fatalf("sync: %v", err)
	}

	today, err := events.ListToday(ctx)
	if err != nil {
		t.Fatalf("ListToday: %v", err)
	}
	if len(today) != 0 {
		t.Fatalf("expected the declined event to be filtered out, got %+v", today)
	}
}

// TestSyncGoogleAccountTokenRefreshFailureSurfacesAsAccountError confirms an
// invalid_grant-style failure from the token endpoint surfaces as an error
// from the sync call (the caller - scheduler.go/sync.go - records this via
// CalendarAccounts.MarkSynced), rather than panicking or silently no-oping.
func TestSyncGoogleAccountTokenRefreshFailureSurfacesAsAccountError(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}
	accounts := &models.CalendarAccountStore{DB: conn}

	accountID, err := accounts.CreateGoogle(ctx, "Broken Google Account", []byte("unused"), "me@example.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}
	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	oauthCfg := fakeGoogleOAuthConfig(srv)
	err = syncGoogleAccountAgainstFakeServer(ctx, calendars, events, *account, oauthCfg, "revoked-refresh-token", srv, 7)
	if err == nil {
		t.Fatal("expected a revoked/invalid refresh token to surface as an error")
	}
}
