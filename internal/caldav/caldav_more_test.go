package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
)

// --- NewClient / resolveHomeSet / DiscoverCalendars error branches ---

// TestNewClientErrorForInvalidURL covers NewClient's error-wrapping branch:
// a base URL containing an ASCII control character fails url.Parse deep
// inside go-webdav's internal.NewClient, and NewClient must propagate that
// rather than panicking or silently continuing with a nil client.
func TestNewClientErrorForInvalidURL(t *testing.T) {
	_, err := NewClient("http://example.com/\x7f", "user", "password", models.ProviderGeneric)
	if err == nil {
		t.Fatal("expected NewClient to fail for a URL containing a control character")
	}
}

// TestResolveHomeSetPrincipalDiscoveryError covers resolveHomeSet's
// FindCurrentUserPrincipal error branch (non-Fastmail providers only -
// Fastmail skips this call entirely, see the package doc comment).
func TestResolveHomeSetPrincipalDiscoveryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "simulated failure", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "user", "password", models.ProviderICloud)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.resolveHomeSet(context.Background()); err == nil {
		t.Fatal("expected resolveHomeSet to propagate a principal-discovery failure")
	}
}

// TestResolveHomeSetFindCalendarHomeSetError covers resolveHomeSet's
// FindCalendarHomeSet error branch. Uses Fastmail (whose principal is
// constructed directly, skipping discovery) so the only PROPFIND issued is
// the calendar-home-set lookup itself, isolating this specific failure.
func TestResolveHomeSetFindCalendarHomeSetError(t *testing.T) {
	const username = "someone@example.com"
	wantPrincipal := "/dav/principals/user/" + username + "/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPrincipal {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		http.Error(w, "simulated failure", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, username, "app-password", models.ProviderFastmail)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.resolveHomeSet(context.Background()); err == nil {
		t.Fatal("expected resolveHomeSet to propagate a calendar-home-set lookup failure")
	}
}

// TestDiscoverCalendarsPropagatesResolveHomeSetError covers DiscoverCalendars'
// early-return branch when resolveHomeSet itself fails - FindCalendars must
// never be attempted in that case.
func TestDiscoverCalendarsPropagatesResolveHomeSetError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "simulated failure", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "user", "password", models.ProviderGeneric)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.DiscoverCalendars(context.Background()); err == nil {
		t.Fatal("expected DiscoverCalendars to propagate a resolveHomeSet failure")
	}
}

// TestDiscoverCalendarsFindCalendarsError covers DiscoverCalendars' own
// FindCalendars error-wrapping branch: principal/home-set resolution
// succeeds, but the subsequent calendar-listing PROPFIND fails.
func TestDiscoverCalendarsFindCalendarsError(t *testing.T) {
	const principal = "/principals/user/"
	const homeSet = "/calendars/user/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/":
			testutil.WritePrincipalResponse(w, principal)
		case r.Method == "PROPFIND" && r.URL.Path == principal:
			testutil.WriteHomeSetResponse(w, principal, homeSet)
		case r.Method == "PROPFIND" && r.URL.Path == homeSet:
			http.Error(w, "simulated failure", http.StatusInternalServerError)
		default:
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "user", "password", models.ProviderGeneric)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.DiscoverCalendars(context.Background())
	if err == nil {
		t.Fatal("expected DiscoverCalendars to propagate a FindCalendars failure")
	}
	if !strings.Contains(err.Error(), "listing calendars") {
		t.Fatalf("error = %v, want it to include the 'listing calendars' wrap context", err)
	}
}

// TestDiscoverCalendarsFallsBackToPathWhenDisplayNameEmpty covers
// DiscoverCalendars' fallback: not every server reports a displayname for
// every calendar, so the calendar's path is used as its name instead of
// leaving it blank in the parent dashboard.
func TestDiscoverCalendarsFallsBackToPathWhenDisplayNameEmpty(t *testing.T) {
	const principal = "/principals/user/"
	const homeSet = "/calendars/user/"
	const calPath = homeSet + "noname/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/":
			testutil.WritePrincipalResponse(w, principal)
		case r.Method == "PROPFIND" && r.URL.Path == principal:
			testutil.WriteHomeSetResponse(w, principal, homeSet)
		case r.Method == "PROPFIND" && r.URL.Path == homeSet:
			testutil.WriteCalendarListResponse(w, homeSet, map[string]string{calPath: ""})
		default:
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "user", "password", models.ProviderGeneric)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	discovered, err := client.DiscoverCalendars(context.Background())
	if err != nil {
		t.Fatalf("DiscoverCalendars: %v", err)
	}
	if len(discovered) != 1 {
		t.Fatalf("got %d calendars, want 1", len(discovered))
	}
	if discovered[0].Name != calPath {
		t.Fatalf("Name = %q, want fallback to path %q when the server reports no displayname", discovered[0].Name, calPath)
	}
}

// --- flattenEvents branches not covered by caldav_test.go ---

// TestFlattenEventsSkipsNonEventComponents covers flattenEvents' guard
// against non-VEVENT children (e.g. VTIMEZONE, which every real calendar
// response includes) - only actual VEVENT components should be extracted.
func TestFlattenEventsSkipsNonEventComponents(t *testing.T) {
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	tz := ical.NewComponent("VTIMEZONE")
	tz.Props.SetText(ical.PropTimezoneID, "America/New_York")

	cal := &ical.Calendar{Component: &ical.Component{
		Name: ical.CompCalendar,
		Children: []*ical.Component{
			tz,
			newTestEventComponent("event-1@example.com", "Real Event", "", start, end, false),
		},
	}}

	events := flattenEvents(cal)
	if len(events) != 1 || events[0].UID != "event-1@example.com" {
		t.Fatalf("expected only the VEVENT component to be extracted, got %+v", events)
	}
}

// TestFlattenEventsParsesOrganizerAttendeesAndAttachments covers the
// ORGANIZER/ATTENDEE/ATTACH parsing branches, all of which round-trip via
// the explicit prop list FetchEventsForCalendar requests (round 13's
// event-detail feature).
func TestFlattenEventsParsesOrganizerAttendeesAndAttachments(t *testing.T) {
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	comp := newTestEventComponent("full-event@example.com", "Full Event", "Kitchen Table", start, end, false)

	org := ical.NewProp(ical.PropOrganizer)
	org.Value = "mailto:parent@example.com"
	org.Params.Set(ical.ParamCommonName, "The Parent")
	comp.Props.Set(org)

	att1 := ical.NewProp(ical.PropAttendee)
	att1.Value = "mailto:child@example.com"
	att1.Params.Set(ical.ParamCommonName, "The Child")
	att1.Params.Set(ical.ParamParticipationStatus, "ACCEPTED")
	comp.Props.Add(att1)

	att2 := ical.NewProp(ical.PropAttendee)
	att2.Value = "mailto:other@example.com"
	comp.Props.Add(att2)

	attach := ical.NewProp(ical.PropAttach)
	attach.Value = "https://example.com/files/invoice.pdf"
	attach.Params.Set(ical.ParamFormatType, "application/pdf")
	comp.Props.Add(attach)

	cal := &ical.Calendar{Component: &ical.Component{
		Name:     ical.CompCalendar,
		Children: []*ical.Component{comp},
	}}

	events := flattenEvents(cal)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]

	if e.OrganizerName != "The Parent" || e.OrganizerEmail != "parent@example.com" {
		t.Fatalf("organizer = (%q, %q), want (%q, %q)", e.OrganizerName, e.OrganizerEmail, "The Parent", "parent@example.com")
	}
	if len(e.Attendees) != 2 {
		t.Fatalf("got %d attendees, want 2: %+v", len(e.Attendees), e.Attendees)
	}
	if e.Attendees[0].Name != "The Child" || e.Attendees[0].Email != "child@example.com" || e.Attendees[0].Status != "ACCEPTED" {
		t.Fatalf("attendee[0] = %+v", e.Attendees[0])
	}
	if e.Attendees[1].Email != "other@example.com" || e.Attendees[1].Name != "" {
		t.Fatalf("attendee[1] = %+v", e.Attendees[1])
	}
	if len(e.Attachments) != 1 || e.Attachments[0].Name != "application/pdf" || e.Attachments[0].URI != "https://example.com/files/invoice.pdf" {
		t.Fatalf("attachments = %+v", e.Attachments)
	}
}

// TestFlattenEventsSkipsWhenDateTimeStartUnparseable covers the branch
// where a VEVENT has a UID but an unparseable DTSTART (distinct from a
// missing UID, covered elsewhere) - such a component must be dropped
// rather than producing a zero-value start time.
func TestFlattenEventsSkipsWhenDateTimeStartUnparseable(t *testing.T) {
	comp := ical.NewComponent(ical.CompEvent)
	comp.Props.SetText(ical.PropUID, "bad-start@example.com")
	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = "not-a-valid-date" // wrong length/format for any known value type
	comp.Props.Set(startProp)

	cal := &ical.Calendar{Component: &ical.Component{
		Name:     ical.CompCalendar,
		Children: []*ical.Component{comp},
	}}

	events := flattenEvents(cal)
	if len(events) != 0 {
		t.Fatalf("expected an event with an unparseable DTSTART to be skipped, got %+v", events)
	}
}

// TestFlattenEventsFallsBackToOneHourWhenDateTimeEndUnparseable covers the
// `end = start.Add(time.Hour)` fallback - distinct from the "no DTEND at
// all" case in caldav_test.go, which go-ical resolves to start+0 without
// erroring. This exercises an actually malformed DTEND value, which
// go-ical's DateTimeEnd does return an error for.
func TestFlattenEventsFallsBackToOneHourWhenDateTimeEndUnparseable(t *testing.T) {
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	comp := ical.NewComponent(ical.CompEvent)
	comp.Props.SetText(ical.PropUID, "bad-end@example.com")
	comp.Props.SetDateTime(ical.PropDateTimeStart, start)
	endProp := ical.NewProp(ical.PropDateTimeEnd)
	endProp.Value = "also-not-a-valid-date"
	comp.Props.Set(endProp)

	cal := &ical.Calendar{Component: &ical.Component{
		Name:     ical.CompCalendar,
		Children: []*ical.Component{comp},
	}}

	events := flattenEvents(cal)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if !events[0].EndsAt.Equal(start.Add(time.Hour)) {
		t.Fatalf("EndsAt = %v, want start + 1h fallback when DTEND is unparseable", events[0].EndsAt)
	}
}

// --- DiscoverAndSyncAccount / DiscoverAndUpsertCalendars error branches ---

func TestDiscoverAndSyncAccountMissingCredentialsErrors(t *testing.T) {
	err := DiscoverAndSyncAccount(context.Background(), &models.CalendarStore{}, &models.EventStore{},
		models.CalendarAccount{Name: "Broken"}, "password", 7)
	if err == nil {
		t.Fatal("expected an error for an account missing CalDAV URL/username")
	}
}

func TestDiscoverAndSyncAccountNewClientErrorPropagates(t *testing.T) {
	account := models.CalendarAccount{
		Name:      "Bad URL",
		Provider:  models.ProviderGeneric,
		CalDAVURL: sqlNullString("http://example.com/\x7f"),
		Username:  sqlNullString("user"),
	}
	err := DiscoverAndSyncAccount(context.Background(), &models.CalendarStore{}, &models.EventStore{}, account, "password", 7)
	if err == nil {
		t.Fatal("expected NewClient's URL-parse failure to propagate")
	}
}

func TestDiscoverAndSyncAccountPropagatesDiscoveryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "simulated failure", http.StatusInternalServerError)
	}))
	defer srv.Close()

	account := models.CalendarAccount{
		Name:      "Test Account",
		Provider:  models.ProviderGeneric,
		CalDAVURL: sqlNullString(srv.URL),
		Username:  sqlNullString("user"),
	}
	err := DiscoverAndSyncAccount(context.Background(), &models.CalendarStore{}, &models.EventStore{}, account, "password", 7)
	if err == nil {
		t.Fatal("expected a discovery failure to propagate from DiscoverAndSyncAccount")
	}
}

// TestDiscoverAndSyncAccountSkipsDisabledCalendars is a direct regression
// test for the disabled-calendar skip in DiscoverAndSyncAccount's fetch
// loop: a calendar disabled after its first sync must not have its events
// re-fetched on a later sync pass.
func TestDiscoverAndSyncAccountSkipsDisabledCalendars(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	const principal = "/principals/user/"
	const homeSet = "/calendars/user/"
	const calPath = homeSet + "home/"

	var reportCalls int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/":
			testutil.WritePrincipalResponse(w, principal)
		case r.Method == "PROPFIND" && r.URL.Path == principal:
			testutil.WriteHomeSetResponse(w, principal, homeSet)
		case r.Method == "PROPFIND" && r.URL.Path == homeSet:
			testutil.WriteCalendarListResponse(w, homeSet, map[string]string{calPath: "Home"})
		case r.Method == "REPORT" && r.URL.Path == calPath:
			atomic.AddInt32(&reportCalls, 1)
			testutil.WriteEmptyCalendarQueryResponse(w)
		default:
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	accounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}

	accountID, err := accounts.Create(ctx, models.CalendarAccount{
		Name:              "Test Account",
		Provider:          models.ProviderGeneric,
		CalDAVURL:         sqlNullString(srv.URL),
		Username:          sqlNullString("user"),
		EncryptedPassword: []byte("unused-in-this-test"),
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}
	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID account: %v", err)
	}

	if err := DiscoverAndSyncAccount(ctx, calendars, events, *account, "password", 7); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if got := atomic.LoadInt32(&reportCalls); got != 1 {
		t.Fatalf("expected 1 REPORT call after the first (enabled) sync, got %d", got)
	}

	cals, err := calendars.ListAllWithAccount(ctx)
	if err != nil || len(cals) != 1 {
		t.Fatalf("ListAllWithAccount: cals=%+v err=%v", cals, err)
	}
	if err := calendars.SetEnabled(ctx, cals[0].ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	if err := DiscoverAndSyncAccount(ctx, calendars, events, *account, "password", 7); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if got := atomic.LoadInt32(&reportCalls); got != 1 {
		t.Fatalf("expected REPORT call count to stay at 1 after disabling the calendar, got %d", got)
	}
}

func TestDiscoverAndUpsertCalendarsNewClientErrorPropagates(t *testing.T) {
	account := models.CalendarAccount{
		Name:      "Bad URL",
		Provider:  models.ProviderGeneric,
		CalDAVURL: sqlNullString("http://example.com/\x7f"),
		Username:  sqlNullString("user"),
	}
	_, err := DiscoverAndUpsertCalendars(context.Background(), &models.CalendarStore{}, account, "password")
	if err == nil {
		t.Fatal("expected NewClient's URL-parse failure to propagate")
	}
}

// TestDiscoverAndUpsertCalendarsWrapsDiscoveryError covers discoverAndUpsert's
// own error-wrapping branch (as reached via the DiscoverAndUpsertCalendars
// entry point), verifying the wrapped message identifies the account.
func TestDiscoverAndUpsertCalendarsWrapsDiscoveryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "simulated failure", http.StatusInternalServerError)
	}))
	defer srv.Close()

	account := models.CalendarAccount{
		Name:      "Test Account",
		Provider:  models.ProviderGeneric,
		CalDAVURL: sqlNullString(srv.URL),
		Username:  sqlNullString("user"),
	}
	_, err := DiscoverAndUpsertCalendars(context.Background(), &models.CalendarStore{}, account, "password")
	if err == nil {
		t.Fatal("expected a wrapped discovery error")
	}
	if !strings.Contains(err.Error(), "discovering calendars for") {
		t.Fatalf("error = %v, want it to include discoverAndUpsert's wrap context", err)
	}
}

// TestDiscoverAndUpsertSkipsCalendarWhenUpsertFails covers discoverAndUpsert's
// UpsertDiscovered-error branch against a real Postgres instance: an
// account ID that doesn't exist makes the INSERT's calendar_account_id
// foreign key fail, and the failure must be logged and skipped rather than
// aborting discovery of the account's other calendars.
func TestDiscoverAndUpsertSkipsCalendarWhenUpsertFails(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	const principal = "/principals/user/"
	const homeSet = "/calendars/user/"
	const calPath = homeSet + "home/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/":
			testutil.WritePrincipalResponse(w, principal)
		case r.Method == "PROPFIND" && r.URL.Path == principal:
			testutil.WriteHomeSetResponse(w, principal, homeSet)
		case r.Method == "PROPFIND" && r.URL.Path == homeSet:
			testutil.WriteCalendarListResponse(w, homeSet, map[string]string{calPath: "Home"})
		default:
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "user", "password", models.ProviderGeneric)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	calendars := &models.CalendarStore{DB: conn}
	account := models.CalendarAccount{ID: 999999, Name: "Ghost Account"}

	result, err := discoverAndUpsert(ctx, calendars, client, account)
	if err != nil {
		t.Fatalf("discoverAndUpsert: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected the failed upsert to be skipped rather than returned, got %+v", result)
	}
}

// TestSyncCalendarEventsRecordsUpsertFailure covers syncCalendarEvents'
// events.Upsert-error branch against a real Postgres instance: a calendar
// ID that doesn't exist makes the event insert's calendar_id foreign key
// fail. The function must not panic, and must not have stored the event.
func TestSyncCalendarEventsRecordsUpsertFailure(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	const calPath = "/calendars/user/home/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "REPORT" && r.URL.Path == calPath {
			writeCalendarQueryResponse(w, calPath+"event-1.ics",
				newTestEventComponent("event-1@example.com", "Ghost Calendar Event", "",
					time.Now(), time.Now().Add(time.Hour), false))
			return
		}
		http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "user", "password", models.ProviderGeneric)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}
	cal := models.Calendar{ID: 999999, Name: "Ghost Calendar", ExternalPath: calPath}

	syncCalendarEvents(ctx, calendars, events, cal, client, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	var count int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM hhq_calendar_events_cache WHERE uid = $1`, "event-1@example.com").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the event to not be stored after the foreign key violation, got count=%d", count)
	}
}
