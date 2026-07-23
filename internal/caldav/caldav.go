// Package caldav syncs events from CalDAV calendar sources (Fastmail, iCloud, or
// any standards-compliant CalDAV server) into the local calendar_events_cache table.
//
// Both Fastmail and iCloud speak standard CalDAV with HTTP Basic Auth using an
// app-specific password (NOT your account password):
//   - Fastmail: https://www.fastmail.com/settings/security/apppasswords -> create one
//     scoped to "Calendars (CalDAV)". Principal URL is typically
//     https://caldav.fastmail.com/dav/calendars/user/<your-email>/
//   - iCloud:   https://appleid.apple.com -> Sign-In and Security -> App-Specific
//     Passwords. Principal URL is https://caldav.icloud.com/ (the client must
//     follow the principal/home-set discovery, which go-webdav handles for you).
//
// # Fastmail-specific 405 workaround
//
// go-webdav's FindCurrentUserPrincipal always issues its discovery PROPFIND
// against path.Join(basePath, "") - and Go's path.Join silently strips any
// trailing slash (confirmed by inspecting go-webdav's internal ResolveHref).
// So even if the configured base URL is "https://caldav.fastmail.com/dav/"
// (with a trailing slash), the actual request goes to "/dav" (without one).
// Fastmail's CalDAV server (Cyrus IMAP) treats that as a different, invalid
// resource and returns 405 Method Not Allowed instead of a redirect - this
// happens no matter which base URL variant is configured, which is why
// trying both "with" and "without" a trailing /dav/ doesn't help; the bug is
// in how the discovery request itself gets built, not in the configured URL.
//
// Fastmail's principal URL scheme is stable and documented
// (https://caldav.fastmail.com/dav/principals/user/<email>/), so for
// provider=Fastmail we skip discovery entirely and construct that path
// directly from the account's username, going straight to the
// calendar-home-set lookup. iCloud and generic CalDAV servers still use
// standard discovery, which works fine against them.
//
// # calendar-query property requests
//
// Requesting event data (the calendar-query REPORT) requires an explicit
// list of iCalendar properties (UID, SUMMARY, LOCATION, DTSTART, DTEND,
// DURATION) rather than <C:allprop/>. AllProps is valid per RFC 4791 and
// Cyrus (Fastmail) honors it, but Apple's iCloud CalDAV server does not
// reliably honor <C:allprop/> nested inside a <C:comp> - it returns 200 OK
// with the component present but silently empty of properties. An explicit
// property list matches Apple's own documented working CalDAV examples and
// is also the RFC's canonical calendar-query example, and continues to work
// against Cyrus too.
package caldav

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	icaldav "github.com/emersion/go-webdav/caldav"

	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
)

func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// basicAuthTransport injects HTTP Basic Auth into every request — both Fastmail
// and iCloud authenticate this way when using app-specific passwords.
type basicAuthTransport struct {
	username, password string
	base               http.RoundTripper
}

func (t *basicAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(t.username, t.password)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// Client wraps a go-webdav CalDAV client for one calendar account.
type Client struct {
	cd       *icaldav.Client
	username string
	provider models.CalendarProvider
}

// NewClient builds a CalDAV client. baseURL should be the server's CalDAV root
// or principal URL (see provider notes above) — the underlying library handles
// principal/calendar-home-set discovery from there, except for Fastmail (see
// the package doc comment for why that provider skips discovery).
func NewClient(baseURL, username, password string, provider models.CalendarProvider) (*Client, error) {
	httpClient := &http.Client{
		Transport: &basicAuthTransport{username: username, password: password},
		Timeout:   30 * time.Second,
	}
	cd, err := icaldav.NewClient(httpClient, baseURL)
	if err != nil {
		return nil, fmt.Errorf("creating caldav client: %w", err)
	}
	return &Client{cd: cd, username: username, provider: provider}, nil
}

// resolveHomeSet performs principal discovery (or the Fastmail-specific
// bypass documented above) and returns the calendar-home-set path. Shared by
// DiscoverCalendars; kept as its own method so the discovery logic - and its
// provider-specific quirks - lives in exactly one place.
func (c *Client) resolveHomeSet(ctx context.Context) (string, error) {
	var principal string

	if c.provider == models.ProviderFastmail {
		principal = "/dav/principals/user/" + c.username + "/"
		logging.Debugf("caldav: using known Fastmail principal path %q (discovery skipped - see package docs)", principal)
	} else {
		var err error
		principal, err = c.cd.FindCurrentUserPrincipal(ctx)
		if err != nil {
			return "", fmt.Errorf("finding current user principal: %w", err)
		}
		logging.Debugf("caldav: resolved principal %q", principal)
	}

	homeSet, err := c.cd.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return "", fmt.Errorf("finding calendar home set: %w", err)
	}
	logging.Debugf("caldav: resolved calendar home set %q", homeSet)
	return homeSet, nil
}

// DiscoveredCalendar is a lightweight result of listing the calendars under
// an account, without fetching any event data - used to populate/refresh the
// `calendars` table (see models.CalendarStore.UpsertDiscovered).
type DiscoveredCalendar struct {
	Path string
	Name string
}

// DiscoverCalendars lists the calendars available under this account.
func (c *Client) DiscoverCalendars(ctx context.Context) ([]DiscoveredCalendar, error) {
	homeSet, err := c.resolveHomeSet(ctx)
	if err != nil {
		return nil, err
	}

	cals, err := c.cd.FindCalendars(ctx, homeSet)
	if err != nil {
		return nil, fmt.Errorf("listing calendars: %w", err)
	}
	logging.Debugf("caldav: found %d calendar(s) under %q", len(cals), homeSet)

	out := make([]DiscoveredCalendar, 0, len(cals))
	for _, cal := range cals {
		name := cal.Name
		if name == "" {
			// Not every server reports a displayname for every calendar;
			// fall back to the path so it's at least distinguishable rather
			// than showing a blank name in the parent dashboard.
			name = cal.Path
		}
		out = append(out, DiscoveredCalendar{Path: cal.Path, Name: name})
	}
	return out, nil
}

// FetchedEvent is a flattened representation of one VEVENT, ready to store.
type FetchedEvent struct {
	UID            string
	Summary        string
	Location       string
	Description    string
	OrganizerName  string
	OrganizerEmail string
	Attendees      []models.Attendee
	Attachments    []models.Attachment
	StartsAt       time.Time
	EndsAt         time.Time
	AllDay         bool
}

// FetchEventsForCalendar queries a single, already-known calendar path for
// VEVENTs whose time falls within [windowStart, windowEnd). For recurring
// events, this returns each materialized occurrence within the window
// (go-webdav's calendar-query with a time-range filter handles expansion
// against the server).
func (c *Client) FetchEventsForCalendar(ctx context.Context, calendarPath string, windowStart, windowEnd time.Time) ([]FetchedEvent, error) {
	query := &icaldav.CalendarQuery{
		// CompRequest tells the server what to actually return in the
		// response (the <D:prop><C:calendar-data>...) - without it, this
		// defaults to a zero-value CalendarCompRequest, which encodes as an
		// empty <C:comp name=""/> and causes Cyrus (Fastmail) to reject the
		// request with 400 Bad Request. An explicit property list (rather
		// than AllProps) is required for iCloud - see the package doc
		// comment for the full explanation of both quirks.
		CompRequest: icaldav.CalendarCompRequest{
			Name: "VCALENDAR",
			Comps: []icaldav.CalendarCompRequest{
				{
					Name: "VEVENT",
					Props: []string{
						"UID",
						"SUMMARY",
						"LOCATION",
						"DESCRIPTION",
						"ORGANIZER",
						"ATTENDEE",
						"ATTACH",
						"DTSTART",
						"DTEND",
						"DURATION", // fallback go-ical's DateTimeEnd uses when DTEND is absent
					},
				},
			},
		},
		CompFilter: icaldav.CompFilter{
			Name: "VCALENDAR",
			Comps: []icaldav.CompFilter{
				{
					Name:  "VEVENT",
					Start: windowStart,
					End:   windowEnd,
				},
			},
		},
	}

	objs, err := c.cd.QueryCalendar(ctx, calendarPath, query)
	if err != nil {
		return nil, fmt.Errorf("querying calendar %q: %w", calendarPath, err)
	}
	logging.Debugf("caldav: calendar %q returned %d object(s) in window", calendarPath, len(objs))

	var results []FetchedEvent
	for _, obj := range objs {
		results = append(results, flattenEvents(obj.Data)...)
	}
	return results, nil
}

// flattenEvents extracts VEVENT components from a parsed iCalendar object.
func flattenEvents(cal *ical.Calendar) []FetchedEvent {
	var out []FetchedEvent
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent {
			continue
		}
		ev := ical.Event{Component: comp}

		uid, err := ev.Props.Text(ical.PropUID)
		if err != nil || uid == "" {
			continue
		}
		summary, _ := ev.Props.Text(ical.PropSummary)
		location, _ := ev.Props.Text(ical.PropLocation)
		description, _ := ev.Props.Text(ical.PropDescription)

		var organizerName, organizerEmail string
		if org := comp.Props.Get(ical.PropOrganizer); org != nil {
			organizerName = org.Params.Get(ical.ParamCommonName)
			organizerEmail = strings.TrimPrefix(org.Value, "mailto:")
		}

		var attendees []models.Attendee
		for _, att := range comp.Props[ical.PropAttendee] {
			attendees = append(attendees, models.Attendee{
				Name:   att.Params.Get(ical.ParamCommonName),
				Email:  strings.TrimPrefix(att.Value, "mailto:"),
				Status: att.Params.Get(ical.ParamParticipationStatus),
			})
		}

		var attachments []models.Attachment
		for _, att := range comp.Props[ical.PropAttach] {
			attachments = append(attachments, models.Attachment{
				Name: att.Params.Get(ical.ParamFormatType),
				URI:  att.Value,
			})
		}

		start, err := ev.DateTimeStart(time.Local)
		if err != nil {
			continue
		}
		end, err := ev.DateTimeEnd(time.Local)
		if err != nil {
			end = start.Add(time.Hour) // fallback if no explicit end
		}

		allDay := false
		if startProp := comp.Props.Get(ical.PropDateTimeStart); startProp != nil {
			// All-day events use VALUE=DATE rather than DATE-TIME.
			allDay = startProp.Params.Get(ical.ParamValue) == "DATE"
		}

		out = append(out, FetchedEvent{
			UID:            uid,
			Summary:        summary,
			Location:       location,
			Description:    description,
			OrganizerName:  organizerName,
			OrganizerEmail: organizerEmail,
			Attendees:      attendees,
			Attachments:    attachments,
			StartsAt:       start,
			EndsAt:         end,
			AllDay:         allDay,
		})
	}
	return out
}

// DiscoverAndSyncAccount performs a full pass for one account: discovers its
// calendars (creating new Calendar rows with a freshly-assigned color, or
// just refreshing the display name of ones that already exist - color and
// enabled/disabled are preserved on existing rows), then fetches events for
// every ENABLED calendar and updates the cache.
//
// Returns an error only for account-level failures (e.g. bad credentials)
// that prevented discovery from running at all. Per-calendar event-fetch
// failures are logged and recorded on that calendar's own row via
// calendars.MarkSynced, but don't abort syncing the account's other
// calendars - one broken calendar shouldn't take down the rest.
func DiscoverAndSyncAccount(ctx context.Context, calendars *models.CalendarStore, events *models.EventStore, account models.CalendarAccount, password string, windowDays int) error {
	if !account.CalDAVURL.Valid || !account.Username.Valid {
		return fmt.Errorf("account %q is missing a CalDAV URL or username", account.Name)
	}

	logging.Debugf("caldav: starting discovery+sync for account %q (provider=%s, url=%s)", account.Name, account.Provider, account.CalDAVURL.String)

	client, err := NewClient(account.CalDAVURL.String, account.Username.String, password, account.Provider)
	if err != nil {
		return err
	}

	discoveredCalendars, err := discoverAndUpsert(ctx, calendars, client, account)
	if err != nil {
		return err
	}

	now := time.Now()
	windowStart := now.Truncate(24 * time.Hour) // today, since "today's events" should show even if already passed
	windowEnd := windowStart.AddDate(0, 0, windowDays)

	for _, cal := range discoveredCalendars {
		if !cal.Enabled {
			logging.Debugf("caldav: calendar %q is disabled, skipping event fetch", cal.Name)
			continue
		}
		syncCalendarEvents(ctx, calendars, events, cal, client, windowStart, windowEnd)
	}

	return nil
}

// DiscoverAndUpsertCalendars runs just the discovery step (list calendars,
// create/refresh Calendar rows) without fetching any event data. Used
// synchronously right after a parent adds or edits a calendar account, so
// the calendar list - with freshly assigned colors - appears immediately in
// the dashboard rather than waiting for the full (potentially slower,
// per-calendar event-fetching) background sync to finish.
func DiscoverAndUpsertCalendars(ctx context.Context, calendars *models.CalendarStore, account models.CalendarAccount, password string) ([]models.Calendar, error) {
	if !account.CalDAVURL.Valid || !account.Username.Valid {
		return nil, fmt.Errorf("account %q is missing a CalDAV URL or username", account.Name)
	}
	client, err := NewClient(account.CalDAVURL.String, account.Username.String, password, account.Provider)
	if err != nil {
		return nil, err
	}
	return discoverAndUpsert(ctx, calendars, client, account)
}

// discoverAndUpsert is the shared discovery+upsert core used by both
// DiscoverAndSyncAccount (full sync) and DiscoverAndUpsertCalendars
// (discovery only) - keeping this logic in exactly one place means a color
// or upsert-conflict-handling change only ever needs to happen once.
func discoverAndUpsert(ctx context.Context, calendars *models.CalendarStore, client *Client, account models.CalendarAccount) ([]models.Calendar, error) {
	discovered, err := client.DiscoverCalendars(ctx)
	if err != nil {
		return nil, fmt.Errorf("discovering calendars for %q: %w", account.Name, err)
	}
	logging.Debugf("caldav: account %q has %d discoverable calendar(s)", account.Name, len(discovered))

	result := make([]models.Calendar, 0, len(discovered))
	for _, dc := range discovered {
		color, colorErr := calendars.NextAvailableColor(ctx)
		if colorErr != nil {
			// A color-assignment failure shouldn't block the calendar from
			// being usable - fall back to a sane default and keep going.
			logging.Errorf("caldav: picking a color for new calendar %q: %v", dc.Name, colorErr)
			color = "#3B82F6"
		}

		calID, upsertErr := calendars.UpsertDiscovered(ctx, account.ID, dc.Path, dc.Name, color)
		if upsertErr != nil {
			logging.Errorf("caldav: storing discovered calendar %q: %v", dc.Name, upsertErr)
			continue
		}

		cal, getErr := calendars.GetByID(ctx, calID)
		if getErr != nil {
			logging.Errorf("caldav: reloading calendar id=%d after upsert: %v", calID, getErr)
			continue
		}
		result = append(result, *cal)
	}
	return result, nil
}

// syncCalendarEvents fetches and stores events for one already-known,
// enabled calendar. Any error is logged and recorded on the calendar's row
// rather than returned, since a single calendar's failure shouldn't abort
// the rest of the account's sync (see DiscoverAndSyncAccount).
func syncCalendarEvents(ctx context.Context, calendars *models.CalendarStore, events *models.EventStore, cal models.Calendar, client *Client, windowStart, windowEnd time.Time) {
	fetched, err := client.FetchEventsForCalendar(ctx, cal.ExternalPath, windowStart, windowEnd)
	if err != nil {
		logging.Warnf("caldav: fetching events for calendar %q failed: %v", cal.Name, err)
		_ = calendars.MarkSynced(ctx, cal.ID, err)
		return
	}

	seenUIDs := make([]string, 0, len(fetched))
	for _, fe := range fetched {
		seenUIDs = append(seenUIDs, fe.UID)
		if err := events.Upsert(ctx, models.Event{
			CalendarID:     cal.ID,
			UID:            fe.UID,
			Summary:        fe.Summary,
			Location:       nullableString(fe.Location),
			Description:    nullableString(fe.Description),
			OrganizerName:  nullableString(fe.OrganizerName),
			OrganizerEmail: nullableString(fe.OrganizerEmail),
			Attendees:      fe.Attendees,
			Attachments:    fe.Attachments,
			StartsAt:       fe.StartsAt,
			EndsAt:         fe.EndsAt,
			AllDay:         fe.AllDay,
		}); err != nil {
			logging.Errorf("caldav: storing event %q for calendar %q: %v", fe.UID, cal.Name, err)
			_ = calendars.MarkSynced(ctx, cal.ID, err)
			return
		}
	}

	if err := events.PruneStale(ctx, cal.ID, seenUIDs); err != nil {
		logging.Errorf("caldav: pruning stale events for calendar %q: %v", cal.Name, err)
		_ = calendars.MarkSynced(ctx, cal.ID, err)
		return
	}

	logging.Infof("caldav: synced calendar %q successfully (%d events)", cal.Name, len(fetched))
	_ = calendars.MarkSynced(ctx, cal.ID, nil)
}
