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

package caldav

import (
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
)

// --- flattenEvents: pure, no network ---

func newTestEventComponent(uid, summary, location string, start, end time.Time, allDay bool) *ical.Component {
	comp := ical.NewComponent(ical.CompEvent)
	comp.Props.SetText(ical.PropUID, uid)
	comp.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	if summary != "" {
		comp.Props.SetText(ical.PropSummary, summary)
	}
	if location != "" {
		comp.Props.SetText(ical.PropLocation, location)
	}
	if allDay {
		startProp := ical.NewProp(ical.PropDateTimeStart)
		startProp.Params.Set(ical.ParamValue, "DATE")
		startProp.SetDate(start)
		comp.Props.Set(startProp)
	} else {
		comp.Props.SetDateTime(ical.PropDateTimeStart, start)
	}
	comp.Props.SetDateTime(ical.PropDateTimeEnd, end)
	return comp
}

func TestFlattenEventsExtractsBasicFields(t *testing.T) {
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	cal := &ical.Calendar{Component: &ical.Component{
		Name: ical.CompCalendar,
		Children: []*ical.Component{
			newTestEventComponent("event-1@example.com", "Team meeting", "Kitchen Table", start, end, false),
		},
	}}

	events := flattenEvents(cal)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if e.UID != "event-1@example.com" || e.Summary != "Team meeting" || e.Location != "Kitchen Table" {
		t.Fatalf("unexpected event: %+v", e)
	}
	if !e.StartsAt.Equal(start) {
		t.Fatalf("StartsAt = %v, want %v", e.StartsAt, start)
	}
}

func TestFlattenEventsSkipsComponentsWithoutUID(t *testing.T) {
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	comp := ical.NewComponent(ical.CompEvent)
	comp.Props.SetDateTime(ical.PropDateTimeStart, start)
	// deliberately no UID set

	cal := &ical.Calendar{Component: &ical.Component{
		Name:     ical.CompCalendar,
		Children: []*ical.Component{comp},
	}}

	events := flattenEvents(cal)
	if len(events) != 0 {
		t.Fatalf("expected events without a UID to be dropped, got %+v", events)
	}
}

// TestFlattenEventsRoundTripsIcloudEmptyPropsBug is a regression test for
// CLAUDE.md round 6: iCloud returned VEVENT components with zero properties
// when queried with AllProps, which flattenEvents correctly drops (no UID).
// This documents that behavior directly rather than relying only on the
// FetchEventsForCalendar wire-level test below.
func TestFlattenEventsRoundTripsIcloudEmptyPropsBug(t *testing.T) {
	emptyComp := ical.NewComponent(ical.CompEvent) // simulates the empty-Props VEVENT iCloud returned

	cal := &ical.Calendar{Component: &ical.Component{
		Name:     ical.CompCalendar,
		Children: []*ical.Component{emptyComp},
	}}

	events := flattenEvents(cal)
	if len(events) != 0 {
		t.Fatalf("expected an empty-properties VEVENT to be silently dropped (no UID), got %+v", events)
	}
}

func TestFlattenEventsDetectsAllDayEvents(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)

	cal := &ical.Calendar{Component: &ical.Component{
		Name: ical.CompCalendar,
		Children: []*ical.Component{
			newTestEventComponent("allday-1@example.com", "Holiday", "", start, end, true),
		},
	}}

	events := flattenEvents(cal)
	if len(events) != 1 || !events[0].AllDay {
		t.Fatalf("expected an all-day event to be flagged, got %+v", events)
	}
}

// TestFlattenEventsUsesDurationWhenDTENDAbsent covers the fallback CLAUDE.md
// round 6 notes: go-ical's Event.DateTimeEnd falls back to DTSTART+DURATION
// when DTEND is absent, which is why FetchEventsForCalendar explicitly
// requests the DURATION property.
func TestFlattenEventsUsesDurationWhenDTENDAbsent(t *testing.T) {
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	comp := ical.NewComponent(ical.CompEvent)
	comp.Props.SetText(ical.PropUID, "duration-only@example.com")
	comp.Props.SetDateTime(ical.PropDateTimeStart, start)
	durProp := ical.NewProp(ical.PropDuration)
	durProp.SetDuration(90 * time.Minute)
	comp.Props.Set(durProp)
	// deliberately no DTEND

	cal := &ical.Calendar{Component: &ical.Component{
		Name:     ical.CompCalendar,
		Children: []*ical.Component{comp},
	}}

	events := flattenEvents(cal)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if !events[0].EndsAt.Equal(start.Add(90 * time.Minute)) {
		t.Fatalf("EndsAt = %v, want start + DURATION (90m)", events[0].EndsAt)
	}
}

// TestFlattenEventsWithNoEndOrDurationDefaultsToStart documents that when
// both DTEND and DURATION are absent, go-ical's DateTimeEnd returns
// start+0 without erroring — so flattenEvents' `start.Add(time.Hour)`
// fallback is only reached if DateTimeEnd actually errors (e.g. a malformed
// DTEND), not merely "DTEND is missing".
func TestFlattenEventsWithNoEndOrDurationDefaultsToStart(t *testing.T) {
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	comp := ical.NewComponent(ical.CompEvent)
	comp.Props.SetText(ical.PropUID, "no-end@example.com")
	comp.Props.SetDateTime(ical.PropDateTimeStart, start)
	// deliberately no DTEND/DURATION

	cal := &ical.Calendar{Component: &ical.Component{
		Name:     ical.CompCalendar,
		Children: []*ical.Component{comp},
	}}

	events := flattenEvents(cal)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if !events[0].EndsAt.Equal(start) {
		t.Fatalf("EndsAt = %v, want equal to start when neither DTEND nor DURATION is present", events[0].EndsAt)
	}
}

// --- wire-level regression tests (rounds 4-6) ---

// TestResolveHomeSetFastmailBypassesDiscovery is a direct regression test for
// CLAUDE.md round 4: for Fastmail accounts, resolveHomeSet must not perform
// principal discovery at all (which round 4 proved gets mangled by
// path.Join stripping the trailing slash, producing a request Fastmail 405s).
// Instead it should go straight to the known principal path. This test fails
// the request for anything except that known path, so any regression back to
// calling FindCurrentUserPrincipal for Fastmail would show up as a request
// to an unexpected path.
func TestResolveHomeSetFastmailBypassesDiscovery(t *testing.T) {
	const username = "someone@example.com"
	wantPrincipal := "/dav/principals/user/" + username + "/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != wantPrincipal {
			t.Errorf("unexpected PROPFIND to %q; Fastmail should skip discovery and never request anything but the known principal path %q", r.URL.Path, wantPrincipal)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		testutil.WriteHomeSetResponse(w, wantPrincipal, "/dav/calendars/user/"+username+"/")
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, username, "app-password", models.ProviderFastmail)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	homeSet, err := client.resolveHomeSet(context.Background())
	if err != nil {
		t.Fatalf("resolveHomeSet: %v", err)
	}
	wantHomeSet := "/dav/calendars/user/" + username + "/"
	if homeSet != wantHomeSet {
		t.Fatalf("homeSet = %q, want %q", homeSet, wantHomeSet)
	}
}

// TestResolveHomeSetGenericProviderUsesDiscovery confirms non-Fastmail
// providers still go through standard principal discovery (a request to the
// server root), preserving the behavior rounds 4-6 explicitly left alone for
// iCloud/generic CalDAV.
func TestResolveHomeSetGenericProviderUsesDiscovery(t *testing.T) {
	var sawPrincipalDiscovery bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/":
			sawPrincipalDiscovery = true
			testutil.WritePrincipalResponse(w, "/principals/user/")
		case "/principals/user/":
			testutil.WriteHomeSetResponse(w, "/principals/user/", "/calendars/user/")
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "user", "password", models.ProviderICloud)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if _, err := client.resolveHomeSet(context.Background()); err != nil {
		t.Fatalf("resolveHomeSet: %v", err)
	}
	if !sawPrincipalDiscovery {
		t.Fatal("expected a standard principal-discovery PROPFIND for a non-Fastmail provider")
	}
}

// TestFetchEventsForCalendarSendsExplicitPropsNotAllProp is a direct
// regression test for CLAUDE.md rounds 5-6: the REPORT body must (a) set a
// non-empty CompFilter/CompRequest comp name (round 5's 400 fix) and (b) use
// an explicit property list rather than <allprop/> (round 6's iCloud
// empty-properties fix). It inspects the raw request body byte-for-byte,
// mirroring the strict mock server approach CLAUDE.md describes using to
// catch these bugs originally.
func TestFetchEventsForCalendarSendsExplicitPropsNotAllProp(t *testing.T) {
	const calendarPath = "/calendars/user/home/"
	var capturedBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "REPORT" {
			http.Error(w, "unexpected method "+r.Method, http.StatusMethodNotAllowed)
			return
		}
		body := readBody(t, r)
		capturedBody = body

		if strings.Contains(body, "allprop") {
			http.Error(w, "REPORT must not use allprop (breaks against iCloud)", http.StatusBadRequest)
			return
		}
		if strings.Contains(body, `comp name=""`) {
			http.Error(w, "REPORT must not send an empty comp name (breaks against Cyrus/Fastmail)", http.StatusBadRequest)
			return
		}
		for _, want := range []string{"UID", "SUMMARY", "DTSTART", "DTEND"} {
			if !strings.Contains(body, want) {
				http.Error(w, fmt.Sprintf("REPORT missing expected explicit prop %q", want), http.StatusBadRequest)
				return
			}
		}

		writeCalendarQueryResponse(w, calendarPath+"event-1.ics",
			newTestEventComponent("event-1@example.com", "Fake Test Event", "Kitchen Table",
				time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
				time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC), false))
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "user", "password", models.ProviderICloud)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	events, err := client.FetchEventsForCalendar(context.Background(), calendarPath,
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("FetchEventsForCalendar: %v (request body was: %s)", err, capturedBody)
	}
	if len(events) != 1 || events[0].UID != "event-1@example.com" {
		t.Fatalf("events = %+v, want the single fake event", events)
	}
}

func TestDiscoverCalendars(t *testing.T) {
	const principal = "/principals/user/"
	const homeSet = "/calendars/user/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/":
			testutil.WritePrincipalResponse(w, principal)
		case r.Method == "PROPFIND" && r.URL.Path == principal:
			testutil.WriteHomeSetResponse(w, principal, homeSet)
		case r.Method == "PROPFIND" && r.URL.Path == homeSet:
			testutil.WriteCalendarListResponse(w, homeSet, map[string]string{
				homeSet + "home/": "Home",
				homeSet + "work/": "Work",
			})
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
	if len(discovered) != 2 {
		t.Fatalf("got %d calendars, want 2", len(discovered))
	}
}

// --- End-to-end: real Postgres + fake CalDAV server, mirroring the
// verification approach used for the round 4-6 fixes. ---

func TestDiscoverAndSyncAccountEndToEnd(t *testing.T) {
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
		case r.Method == "REPORT" && r.URL.Path == calPath:
			writeCalendarQueryResponse(w, calPath+"event-1.ics",
				newTestEventComponent("test-event-1@example.com", "Fake End-to-End Event", "Kitchen Table",
					time.Now().Truncate(time.Hour), time.Now().Truncate(time.Hour).Add(time.Hour), false))
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
		t.Fatalf("DiscoverAndSyncAccount: %v", err)
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
	if len(today) != 1 || today[0].UID != "test-event-1@example.com" {
		t.Fatalf("synced events = %+v, want the single fake event", today)
	}
}

// TestDiscoverAndUpsertCalendarsOnlyRunsDiscoveryNoEvents exercises the
// discovery-only entry point used synchronously right after a parent adds or
// edits a calendar account (see internal/handlers/parent.go's
// CreateCalendarAccount) - it must upsert calendar rows with auto-assigned
// colors but must NOT issue any REPORT (event-fetch) request.
func TestDiscoverAndUpsertCalendarsOnlyRunsDiscoveryNoEvents(t *testing.T) {
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
		case r.Method == "REPORT":
			http.Error(w, "discovery-only must not fetch events", http.StatusMethodNotAllowed)
		default:
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	accounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}

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

	discovered, err := DiscoverAndUpsertCalendars(ctx, calendars, *account, "password")
	if err != nil {
		t.Fatalf("DiscoverAndUpsertCalendars: %v", err)
	}
	if len(discovered) != 1 || discovered[0].Name != "Home" {
		t.Fatalf("discovered = %+v, want a single 'Home' calendar", discovered)
	}
	if discovered[0].Color == "" {
		t.Error("expected an auto-assigned color")
	}
}

// TestDiscoverAndUpsertCalendarsMissingCredentialsErrors covers the guard
// clause for an account with no CalDAV URL/username configured.
func TestDiscoverAndUpsertCalendarsMissingCredentialsErrors(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()
	calendars := &models.CalendarStore{DB: conn}

	_, err := DiscoverAndUpsertCalendars(ctx, calendars, models.CalendarAccount{Name: "Broken"}, "password")
	if err == nil {
		t.Fatal("expected an error for an account missing CalDAV URL/username")
	}
}

func TestNullableString(t *testing.T) {
	if got := nullableString(""); got.Valid {
		t.Errorf("nullableString(\"\") = %+v, want an invalid/NULL sql.NullString", got)
	}
	got := nullableString("Kitchen Table")
	if !got.Valid || got.String != "Kitchen Table" {
		t.Errorf("nullableString(%q) = %+v, want a valid NullString wrapping it", "Kitchen Table", got)
	}
}

// TestDiscoverAndSyncAccountToleratesPerCalendarEventFetchFailure covers
// syncCalendarEvents' FetchEventsForCalendar-error branch: a REPORT failure
// for one calendar must be recorded on that calendar's row (via MarkSynced)
// rather than propagating up and aborting DiscoverAndSyncAccount entirely -
// see its doc comment on why per-calendar failures are isolated.
func TestDiscoverAndSyncAccountToleratesPerCalendarEventFetchFailure(t *testing.T) {
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
		case r.Method == "REPORT" && r.URL.Path == calPath:
			http.Error(w, "simulated server failure", http.StatusInternalServerError)
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

	// DiscoverAndSyncAccount itself must still succeed (discovery worked) even
	// though the one calendar's event fetch failed.
	if err := DiscoverAndSyncAccount(ctx, calendars, events, *account, "password", 7); err != nil {
		t.Fatalf("DiscoverAndSyncAccount: %v", err)
	}

	cals, err := calendars.ListAllWithAccount(ctx)
	if err != nil || len(cals) != 1 {
		t.Fatalf("ListAllWithAccount: cals=%+v err=%v", cals, err)
	}
	if !cals[0].LastSyncError.Valid {
		t.Fatal("expected the calendar's failed event fetch to be recorded as a sync error")
	}
}

// --- test XML fixture helpers (minimal, hand-rolled per RFC 4791 shapes) ---

func readBody(t *testing.T, r *http.Request) string {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("reading request body: %v", err)
	}
	return string(data)
}

func writeCalendarQueryResponse(w http.ResponseWriter, objectPath string, comp *ical.Component) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//hhq//test fixture//EN")
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Children = append(cal.Children, comp)
	var buf strings.Builder
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		panic(err) // test fixture construction failure, not a runtime path
	}

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(207)
	fmt.Fprintf(w, `<?xml version="1.0"?>
<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:response>
    <D:href>%s</D:href>
    <D:propstat>
      <D:prop><C:calendar-data>%s</C:calendar-data></D:prop>
      <D:status>HTTP/1.1 200 OK</D:status>
    </D:propstat>
  </D:response>
</D:multistatus>`, xmlEscape(objectPath), xmlEscape(buf.String()))
}

func xmlEscape(s string) string {
	var buf strings.Builder
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func sqlNullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: true}
}
