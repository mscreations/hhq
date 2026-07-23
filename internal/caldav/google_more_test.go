package caldav

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	googleapi "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
)

// --- parseGoogleEventDateTime branches not covered by google_test.go ---
// (google_test.go only exercises it indirectly via a valid DateTime and a
// valid Date, both through flattenGoogleEvent's all-day/timed tests.)

func TestParseGoogleEventDateTimeNilReturnsNotOK(t *testing.T) {
	got, allDay, ok := parseGoogleEventDateTime(nil)
	if ok || allDay || !got.IsZero() {
		t.Fatalf("parseGoogleEventDateTime(nil) = (%v, %v, %v), want (zero, false, false)", got, allDay, ok)
	}
}

func TestParseGoogleEventDateTimeMalformedDateTimeReturnsNotOK(t *testing.T) {
	if _, _, ok := parseGoogleEventDateTime(&googleapi.EventDateTime{DateTime: "not-a-valid-rfc3339"}); ok {
		t.Fatal("expected a malformed DateTime value to be rejected")
	}
}

func TestParseGoogleEventDateTimeMalformedDateReturnsNotOK(t *testing.T) {
	if _, _, ok := parseGoogleEventDateTime(&googleapi.EventDateTime{Date: "not-a-date"}); ok {
		t.Fatal("expected a malformed Date value to be rejected")
	}
}

// TestParseGoogleEventDateTimeEmptyReturnsNotOK covers the final fallback
// branch - an EventDateTime that is non-nil but has neither DateTime nor
// Date set, distinct from the nil-pointer case above.
func TestParseGoogleEventDateTimeEmptyReturnsNotOK(t *testing.T) {
	if _, _, ok := parseGoogleEventDateTime(&googleapi.EventDateTime{}); ok {
		t.Fatal("expected an EventDateTime with neither DateTime nor Date set to be rejected")
	}
}

// TestFlattenGoogleEventSkipsWhenStartUnparseable covers flattenGoogleEvent's
// !ok branch for parseGoogleEventDateTime(ev.Start) - an event whose start
// can't be parsed must be dropped rather than stored with a zero time.
func TestFlattenGoogleEventSkipsWhenStartUnparseable(t *testing.T) {
	ev := &googleapi.Event{
		Id:    "bad-start-1",
		Start: &googleapi.EventDateTime{DateTime: "not-a-valid-rfc3339"},
	}
	if _, ok := flattenGoogleEvent(ev); ok {
		t.Fatal("expected an event with an unparseable Start to be dropped")
	}
}

// --- SyncGoogleAccount: disabled-calendar skip ---

// TestSyncGoogleAccountSkipsDisabledCalendars mirrors
// TestDiscoverAndSyncAccountSkipsDisabledCalendars in caldav_more_test.go:
// a calendar disabled after its first sync must not have its events
// re-fetched on a later sync pass.
func TestSyncGoogleAccountSkipsDisabledCalendars(t *testing.T) {
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

	if err := syncGoogleAccountAgainstFakeServer(ctx, calendars, events, *account, oauthCfg, "fake-refresh-token", srv, 7); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if got := atomic.LoadInt32(&eventsCalls); got != 1 {
		t.Fatalf("expected the events call count to stay at 1 after disabling the calendar, got %d", got)
	}
}

// --- googleDiscoverAndUpsert error branches ---

// TestGoogleDiscoverAndUpsertSkipsCalendarWhenUpsertFails covers
// googleDiscoverAndUpsert's UpsertDiscovered-error branch against a real
// Postgres instance: an account ID that doesn't exist makes the INSERT's
// calendar_account_id foreign key fail, and the failure must be logged and
// skipped rather than aborting discovery of the account's other calendars.
func TestGoogleDiscoverAndUpsertSkipsCalendarWhenUpsertFails(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	mux := http.NewServeMux()
	mux.HandleFunc("/users/me/calendarList", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&googleapi.CalendarList{Items: []*googleapi.CalendarListEntry{{Id: "primary", Summary: "Home"}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc, err := googleapi.NewService(ctx, option.WithoutAuthentication(), option.WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	calendars := &models.CalendarStore{DB: conn}
	account := models.CalendarAccount{ID: 999999, Name: "Ghost Account"}

	result, err := googleDiscoverAndUpsert(ctx, calendars, svc, account)
	if err != nil {
		t.Fatalf("googleDiscoverAndUpsert: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected the failed upsert to be skipped rather than returned, got %+v", result)
	}
}

// TestGoogleDiscoverAndUpsertColorFallbackWhenNextAvailableColorFails covers
// googleDiscoverAndUpsert's color-assignment-failure branch, mirroring
// TestDiscoverAndUpsertColorFallbackWhenNextAvailableColorFails in
// caldav_fakedb_test.go (whose fake-driver infrastructure is reused here -
// same package, same helpers).
func TestGoogleDiscoverAndUpsertColorFallbackWhenNextAvailableColorFails(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/users/me/calendarList", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&googleapi.CalendarList{Items: []*googleapi.CalendarListEntry{{Id: "primary", Summary: "Home"}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc, err := googleapi.NewService(context.Background(), option.WithoutAuthentication(), option.WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	db := newFakeDB(t,
		func(query string, args []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(query, "SELECT color FROM hhq_calendars"):
				return nil, errors.New("simulated color-query failure")
			case strings.Contains(query, "INSERT INTO hhq_calendars"):
				return &fakeRows{cols: []string{"id"}, data: [][]driver.Value{{int64(1)}}}, nil
			case strings.Contains(query, "WHERE c.id = $1"):
				return &fakeRows{
					cols: []string{"id", "calendar_account_id", "account_name", "provider", "external_path", "name", "color", "enabled", "last_synced_at", "last_sync_error", "created_at"},
					data: [][]driver.Value{{int64(1), int64(42), "Test Google Account", "google", "primary", "Home", "#3B82F6", true, nil, nil, time.Now()}},
				}, nil
			default:
				return nil, fmt.Errorf("unexpected query: %s", query)
			}
		},
		nil,
	)

	calendars := &models.CalendarStore{DB: db}
	account := models.CalendarAccount{ID: 42, Name: "Test Google Account"}

	result, err := googleDiscoverAndUpsert(context.Background(), calendars, svc, account)
	if err != nil {
		t.Fatalf("googleDiscoverAndUpsert: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d calendars, want 1", len(result))
	}
	if result[0].Color != "#3B82F6" {
		t.Fatalf("Color = %q, want the hardcoded fallback used when NextAvailableColor fails", result[0].Color)
	}
}

// TestGoogleDiscoverAndUpsertSkipsCalendarWhenGetByIDFails mirrors
// TestDiscoverAndUpsertSkipsCalendarWhenGetByIDFails in caldav_fakedb_test.go.
func TestGoogleDiscoverAndUpsertSkipsCalendarWhenGetByIDFails(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/users/me/calendarList", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&googleapi.CalendarList{Items: []*googleapi.CalendarListEntry{{Id: "primary", Summary: "Home"}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc, err := googleapi.NewService(context.Background(), option.WithoutAuthentication(), option.WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	db := newFakeDB(t,
		func(query string, args []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(query, "SELECT color FROM hhq_calendars"):
				return &fakeRows{cols: []string{"color"}}, nil
			case strings.Contains(query, "INSERT INTO hhq_calendars"):
				return &fakeRows{cols: []string{"id"}, data: [][]driver.Value{{int64(1)}}}, nil
			case strings.Contains(query, "WHERE c.id = $1"):
				return nil, errors.New("simulated reload failure")
			default:
				return nil, fmt.Errorf("unexpected query: %s", query)
			}
		},
		nil,
	)

	calendars := &models.CalendarStore{DB: db}
	account := models.CalendarAccount{ID: 42, Name: "Test Google Account"}

	result, err := googleDiscoverAndUpsert(context.Background(), calendars, svc, account)
	if err != nil {
		t.Fatalf("googleDiscoverAndUpsert: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected the calendar to be skipped when reloading it after upsert fails, got %+v", result)
	}
}

// --- googleSyncCalendarEvents error branches ---

// TestGoogleSyncCalendarEventsToleratesFetchFailure mirrors
// TestDiscoverAndSyncAccountToleratesPerCalendarEventFetchFailure in
// caldav_test.go: a per-calendar Events.List failure must be recorded on
// that calendar's row rather than aborting the whole account sync.
func TestGoogleSyncCalendarEventsToleratesFetchFailure(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

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
		http.Error(w, "simulated failure", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
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

	// The account-level sync must still succeed (discovery worked) even
	// though the one calendar's event fetch failed.
	if err := syncGoogleAccountAgainstFakeServer(ctx, calendars, events, *account, oauthCfg, "fake-refresh-token", srv, 7); err != nil {
		t.Fatalf("sync: %v", err)
	}

	cals, err := calendars.ListAllWithAccount(ctx)
	if err != nil || len(cals) != 1 {
		t.Fatalf("ListAllWithAccount: cals=%+v err=%v", cals, err)
	}
	if !cals[0].LastSyncError.Valid {
		t.Fatal("expected the calendar's failed event fetch to be recorded as a sync error")
	}
}

// TestGoogleSyncCalendarEventsRecordsUpsertFailure covers
// googleSyncCalendarEvents' events.Upsert-error branch against a real
// Postgres instance: a calendar ID that doesn't exist makes the event
// insert's calendar_id foreign key fail.
func TestGoogleSyncCalendarEventsRecordsUpsertFailure(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	mux := http.NewServeMux()
	mux.HandleFunc("/calendars/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&googleapi.Events{Items: []*googleapi.Event{
			{
				Id:      "raw-1",
				ICalUID: "ghost-event@google.com",
				Summary: "Ghost Event",
				Start:   &googleapi.EventDateTime{DateTime: time.Now().Format(time.RFC3339)},
				End:     &googleapi.EventDateTime{DateTime: time.Now().Add(time.Hour).Format(time.RFC3339)},
			},
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc, err := googleapi.NewService(ctx, option.WithoutAuthentication(), option.WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}
	cal := models.Calendar{ID: 999999, Name: "Ghost Calendar", ExternalPath: "primary"}

	googleSyncCalendarEvents(ctx, calendars, events, cal, svc, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	var count int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM hhq_calendar_events_cache WHERE uid = $1`, "ghost-event@google.com").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the event to not be stored after the foreign key violation, got count=%d", count)
	}
}

// TestGoogleSyncCalendarEventsRecordsPruneStaleFailure mirrors
// TestSyncCalendarEventsRecordsPruneStaleFailure in caldav_fakedb_test.go,
// reusing its fake-driver infrastructure.
func TestGoogleSyncCalendarEventsRecordsPruneStaleFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/calendars/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&googleapi.Events{Items: []*googleapi.Event{
			{
				Id:      "raw-1",
				ICalUID: "event-1@google.com",
				Summary: "Fake Event",
				Start:   &googleapi.EventDateTime{DateTime: time.Now().Format(time.RFC3339)},
				End:     &googleapi.EventDateTime{DateTime: time.Now().Add(time.Hour).Format(time.RFC3339)},
			},
		}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc, err := googleapi.NewService(context.Background(), option.WithoutAuthentication(), option.WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	var upsertCalled, pruneCalled, markSyncedCalled bool

	db := newFakeDB(t, nil, func(query string, args []driver.NamedValue) (driver.Result, error) {
		switch {
		case strings.Contains(query, "INSERT INTO hhq_calendar_events_cache"):
			upsertCalled = true
			return fakeResult{rowsAffected: 1}, nil
		case strings.Contains(query, "DELETE FROM hhq_calendar_events_cache"):
			pruneCalled = true
			return nil, errors.New("simulated prune failure")
		case strings.Contains(query, "UPDATE hhq_calendars SET last_synced_at"):
			markSyncedCalled = true
			return fakeResult{rowsAffected: 1}, nil
		default:
			return nil, fmt.Errorf("unexpected exec: %s", query)
		}
	})

	calendars := &models.CalendarStore{DB: db}
	events := &models.EventStore{DB: db}
	cal := models.Calendar{ID: 1, Name: "Home", ExternalPath: "primary"}

	googleSyncCalendarEvents(context.Background(), calendars, events, cal, svc,
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	if !upsertCalled {
		t.Error("expected the event Upsert to run before PruneStale")
	}
	if !pruneCalled {
		t.Error("expected PruneStale to be called and fail")
	}
	if !markSyncedCalled {
		t.Error("expected MarkSynced to be called to record PruneStale's failure")
	}
}
