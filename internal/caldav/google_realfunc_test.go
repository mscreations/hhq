package caldav

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	googleapi "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
)

// TestSyncGoogleAccountEndToEnd (google_test.go) and its siblings
// (google_more_test.go) all drive syncGoogleAccountAgainstFakeServer, a
// test-only function that duplicates SyncGoogleAccount's body so the Calendar
// API client can be redirected at a fake httptest server via
// option.WithEndpoint - SyncGoogleAccount itself only ever calls
// option.WithTokenSource, with no way to override the API host, so `go tool
// cover` shows the real SyncGoogleAccount at 0% even though every branch
// inside it is exercised indirectly through the duplicate.
//
// google.go now exposes an unexported googleAPIEndpoint var for exactly this
// case (mirroring the weather package's exported ForecastURL/GeocodeURL
// override vars used the same way, per CLAUDE.md's Round 12 notes) - setting
// it here lets this test call the real, exported SyncGoogleAccount function
// end-to-end, attributing coverage to the actual production code path
// (used for real by internal/scheduler and internal/handlers/sync.go) rather
// than only to the hand-duplicated test helper.
func TestSyncGoogleAccountRealFunctionEndToEnd(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

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
		json.NewEncoder(w).Encode(&googleapi.CalendarList{
			Items: []*googleapi.CalendarListEntry{{Id: "primary", Summary: "Home"}},
		})
	})
	mux.HandleFunc("/calendars/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&googleapi.Events{
			Items: []*googleapi.Event{
				{
					Id:      "raw-1",
					ICalUID: "real-func-event-1@google.com",
					Summary: "Real SyncGoogleAccount Event",
					Start:   &googleapi.EventDateTime{DateTime: time.Now().Format(time.RFC3339)},
					End:     &googleapi.EventDateTime{DateTime: time.Now().Add(time.Hour).Format(time.RFC3339)},
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	original := googleAPIEndpoint
	googleAPIEndpoint = srv.URL
	defer func() { googleAPIEndpoint = original }()

	accounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}

	accountID, err := accounts.CreateGoogle(ctx, "Real Func Test Google Account", []byte("unused-in-this-test"), "me@example.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}
	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	oauthCfg := fakeGoogleOAuthConfig(srv)

	if err := SyncGoogleAccount(ctx, calendars, events, *account, "fake-refresh-token", oauthCfg, 7); err != nil {
		t.Fatalf("SyncGoogleAccount: %v", err)
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
	if len(today) != 1 || today[0].UID != "real-func-event-1@google.com" {
		t.Fatalf("synced events = %+v, want the single fake event", today)
	}
}

// TestSyncGoogleAccountRealFunctionTokenErrorPropagates covers the
// `creating google calendar client` error-wrap branch on the real
// SyncGoogleAccount: a malformed token endpoint response makes the token
// exchange fail, which must surface wrapped with the account's name.
func TestSyncGoogleAccountRealFunctionTokenErrorPropagates(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	original := googleAPIEndpoint
	googleAPIEndpoint = srv.URL
	defer func() { googleAPIEndpoint = original }()

	accounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}

	accountID, err := accounts.CreateGoogle(ctx, "Token Error Test Google Account", []byte("unused-in-this-test"), "me@example.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}
	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	oauthCfg := fakeGoogleOAuthConfig(srv)

	err = SyncGoogleAccount(ctx, calendars, events, *account, "fake-refresh-token", oauthCfg, 7)
	if err == nil {
		t.Fatal("expected SyncGoogleAccount to fail when the token endpoint rejects the refresh token")
	}
}

// TestSyncGoogleAccountRealFunctionSkipsDisabledCalendars is
// TestSyncGoogleAccountSkipsDisabledCalendars's (google_more_test.go)
// sibling, but driving the real, exported SyncGoogleAccount directly (via
// the googleAPIEndpoint override) instead of the hand-duplicated
// syncGoogleAccountAgainstFakeServer helper - covers SyncGoogleAccount's own
// `if !cal.Enabled { continue }` skip branch, which the duplicate-based
// tests can't attribute coverage to since they never call the real function.
func TestSyncGoogleAccountRealFunctionSkipsDisabledCalendars(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	var eventsCalls int32

	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "fake", "token_type": "Bearer", "expires_in": 3600})
	})
	mux.HandleFunc("/users/me/calendarList", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&googleapi.CalendarList{Items: []*googleapi.CalendarListEntry{{Id: "primary", Summary: "Home"}}})
	})
	mux.HandleFunc("/calendars/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&eventsCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&googleapi.Events{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	original := googleAPIEndpoint
	googleAPIEndpoint = srv.URL
	defer func() { googleAPIEndpoint = original }()

	accounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}

	accountID, err := accounts.CreateGoogle(ctx, "Disabled Calendar Real Func Test", []byte("unused-in-this-test"), "me@example.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}
	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	oauthCfg := fakeGoogleOAuthConfig(srv)

	if err := SyncGoogleAccount(ctx, calendars, events, *account, "fake-refresh-token", oauthCfg, 7); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if got := atomic.LoadInt32(&eventsCalls); got != 1 {
		t.Fatalf("expected 1 events call after the first (enabled) sync, got %d", got)
	}

	cals, err := calendars.ListAllWithAccount(ctx)
	if err != nil || len(cals) != 1 {
		t.Fatalf("ListAllWithAccount: cals=%+v err=%v", cals, err)
	}
	if err := calendars.SetEnabled(ctx, cals[0].ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	if err := SyncGoogleAccount(ctx, calendars, events, *account, "fake-refresh-token", oauthCfg, 7); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if got := atomic.LoadInt32(&eventsCalls); got != 1 {
		t.Fatalf("expected the events call count to stay at 1 after disabling the calendar, got %d", got)
	}
}

// TestGoogleDiscoverAndUpsertFallsBackToIDWhenSummaryEmpty covers
// googleDiscoverAndUpsert's `if name == "" { name = entry.Id }` branch: a
// calendar list entry with no Summary (Google allows this, e.g. some
// resource/room calendars) must still get a usable name rather than an
// empty string in the calendars table.
func TestGoogleDiscoverAndUpsertFallsBackToIDWhenSummaryEmpty(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	mux := http.NewServeMux()
	mux.HandleFunc("/users/me/calendarList", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&googleapi.CalendarList{
			Items: []*googleapi.CalendarListEntry{{Id: "no-summary-calendar-id"}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc, err := googleapi.NewService(ctx, option.WithoutAuthentication(), option.WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	accounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	accountID, err := accounts.CreateGoogle(ctx, "No Summary Test Account", []byte("unused"), "me@example.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}
	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	result, err := googleDiscoverAndUpsert(ctx, calendars, svc, *account)
	if err != nil {
		t.Fatalf("googleDiscoverAndUpsert: %v", err)
	}
	if len(result) != 1 || result[0].Name != "no-summary-calendar-id" {
		t.Fatalf("expected the calendar's Id to be used as its Name when Summary is empty, got %+v", result)
	}
}
