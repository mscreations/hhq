// Google Calendar sync via OAuth2. Unlike Fastmail/iCloud (static
// username/app-password, HTTP Basic Auth over CalDAV - see caldav.go), Google
// requires an OAuth2 refresh token obtained through an interactive
// authorization-code flow (see internal/handlers/google_oauth.go). This file
// only handles the sync side once a refresh token already exists; obtaining
// one is the handlers package's job.
package caldav

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/oauth2"
	googleapi "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"

	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
)

// googleAPIEndpoint overrides the Google Calendar API base URL used by
// SyncGoogleAccount when non-empty. Every real deployment leaves this at its
// zero value ("") so google-api-go-client talks to the real googleapis.com
// host as usual; it exists purely so tests can point SyncGoogleAccount at a
// local httptest fake server (via option.WithEndpoint) without SyncGoogleAccount
// having to grow a test-only parameter that every production caller
// (internal/scheduler, internal/handlers/sync.go) would also have to thread
// through for no functional reason.
var googleAPIEndpoint string

// SyncGoogleAccount performs a full discovery+sync pass for one Google
// account, mirroring DiscoverAndSyncAccount's shape/contract for CalDAV
// accounts: discovers the user's calendar list (creating/refreshing Calendar
// rows via UpsertDiscovered, same as CalDAV), then fetches events for every
// enabled calendar within [today, today+windowDays) and upserts them.
//
// refreshToken is the already-decrypted OAuth2 refresh token (see
// models.CalendarAccount.EncryptedRefreshToken / util.Encryptor). oauthCfg
// supplies the client ID/secret needed to exchange it for a short-lived
// access token - see config.GoogleOAuthConfig. The access token itself is
// never persisted; oauth2.TokenSource refreshes it transparently on every
// call, and Google's refresh tokens don't rotate on ordinary background use,
// so there is nothing else to write back after this function returns.
//
// Returns an error only for account-level failures (e.g. a revoked refresh
// token) that prevented discovery from running at all - per-calendar
// failures are logged/recorded on that calendar's row via
// calendars.MarkSynced, same as the CalDAV path.
func SyncGoogleAccount(ctx context.Context, calendars *models.CalendarStore, events *models.EventStore, account models.CalendarAccount, refreshToken string, oauthCfg *oauth2.Config, windowDays int) error {
	tokenSource := oauthCfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	opts := []option.ClientOption{option.WithTokenSource(tokenSource)}
	if googleAPIEndpoint != "" {
		opts = append(opts, option.WithEndpoint(googleAPIEndpoint))
	}
	svc, err := googleapi.NewService(ctx, opts...)
	if err != nil {
		return fmt.Errorf("creating google calendar client for %q: %w", account.Name, err)
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
			logging.Debugf("google: calendar %q is disabled, skipping event fetch", cal.Name)
			continue
		}
		googleSyncCalendarEvents(ctx, calendars, events, cal, svc, windowStart, windowEnd)
	}

	return nil
}

// googleDiscoverAndUpsert lists the account's calendars and upserts them into
// the (provider-agnostic) Calendar model, mirroring discoverAndUpsert in
// caldav.go - same UpsertDiscovered/NextAvailableColor calls, just fed from
// Google's CalendarList API instead of a CalDAV PROPFIND.
func googleDiscoverAndUpsert(ctx context.Context, calendars *models.CalendarStore, svc *googleapi.Service, account models.CalendarAccount) ([]models.Calendar, error) {
	var result []models.Calendar

	err := svc.CalendarList.List().Context(ctx).Pages(ctx, func(page *googleapi.CalendarList) error {
		for _, entry := range page.Items {
			name := entry.Summary
			if name == "" {
				name = entry.Id
			}

			color, colorErr := calendars.NextAvailableColor(ctx)
			if colorErr != nil {
				logging.Errorf("google: picking a color for new calendar %q: %v", name, colorErr)
				color = "#3B82F6"
			}

			calID, upsertErr := calendars.UpsertDiscovered(ctx, account.ID, entry.Id, name, color)
			if upsertErr != nil {
				logging.Errorf("google: storing discovered calendar %q: %v", name, upsertErr)
				continue
			}

			cal, getErr := calendars.GetByID(ctx, calID)
			if getErr != nil {
				logging.Errorf("google: reloading calendar id=%d after upsert: %v", calID, getErr)
				continue
			}
			result = append(result, *cal)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discovering calendars for %q: %w", account.Name, err)
	}
	logging.Debugf("google: account %q has %d discoverable calendar(s)", account.Name, len(result))
	return result, nil
}

// googleSyncCalendarEvents fetches and stores events for one already-known,
// enabled Google calendar. Mirrors syncCalendarEvents in caldav.go: any error
// is logged and recorded on the calendar's row rather than returned, so one
// calendar's failure doesn't abort the rest of the account's sync.
func googleSyncCalendarEvents(ctx context.Context, calendars *models.CalendarStore, events *models.EventStore, cal models.Calendar, svc *googleapi.Service, windowStart, windowEnd time.Time) {
	var fetched []FetchedEvent

	err := svc.Events.List(cal.ExternalPath).
		Context(ctx).
		TimeMin(windowStart.Format(time.RFC3339)).
		TimeMax(windowEnd.Format(time.RFC3339)).
		// SingleEvents expands recurring events into individual occurrences
		// server-side - the Google equivalent of go-webdav's calendar-query
		// time-range filter already relied on for CalDAV recurrence expansion.
		SingleEvents(true).
		Pages(ctx, func(page *googleapi.Events) error {
			for _, ev := range page.Items {
				if fe, ok := flattenGoogleEvent(ev); ok {
					fetched = append(fetched, fe)
				}
			}
			return nil
		})
	if err != nil {
		logging.Warnf("google: fetching events for calendar %q failed: %v", cal.Name, err)
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
			logging.Errorf("google: storing event %q for calendar %q: %v", fe.UID, cal.Name, err)
			_ = calendars.MarkSynced(ctx, cal.ID, err)
			return
		}
	}

	if err := events.PruneStale(ctx, cal.ID, seenUIDs); err != nil {
		logging.Errorf("google: pruning stale events for calendar %q: %v", cal.Name, err)
		_ = calendars.MarkSynced(ctx, cal.ID, err)
		return
	}

	logging.Infof("google: synced calendar %q successfully (%d events)", cal.Name, len(fetched))
	_ = calendars.MarkSynced(ctx, cal.ID, nil)
}

// flattenGoogleEvent maps one Google Calendar API event into the shared
// FetchedEvent shape (the same one CalDAV's flattenEvents produces), or
// returns ok=false to skip it - either it's cancelled, has no usable ID, or
// the connecting account declined it (declined events are hidden from the
// kiosk per product decision, rather than cluttering the family display with
// something nobody plans to attend).
func flattenGoogleEvent(ev *googleapi.Event) (FetchedEvent, bool) {
	if ev.Status == "cancelled" {
		return FetchedEvent{}, false
	}

	uid := ev.ICalUID
	if uid == "" {
		uid = ev.Id
	}
	if uid == "" {
		return FetchedEvent{}, false
	}

	for _, att := range ev.Attendees {
		if att.Self && att.ResponseStatus == "declined" {
			return FetchedEvent{}, false
		}
	}

	start, startAllDay, ok := parseGoogleEventDateTime(ev.Start)
	if !ok {
		return FetchedEvent{}, false
	}
	end, _, ok := parseGoogleEventDateTime(ev.End)
	if !ok {
		end = start.Add(time.Hour) // fallback if no explicit end
	}

	var organizerName, organizerEmail string
	if ev.Organizer != nil {
		organizerName = ev.Organizer.DisplayName
		organizerEmail = ev.Organizer.Email
	}

	var attendees []models.Attendee
	for _, att := range ev.Attendees {
		attendees = append(attendees, models.Attendee{
			Name:   att.DisplayName,
			Email:  att.Email,
			Status: att.ResponseStatus,
		})
	}

	return FetchedEvent{
		UID:            uid,
		Summary:        ev.Summary,
		Location:       ev.Location,
		Description:    ev.Description,
		OrganizerName:  organizerName,
		OrganizerEmail: organizerEmail,
		Attendees:      attendees,
		StartsAt:       start,
		EndsAt:         end,
		AllDay:         startAllDay,
	}, true
}

// parseGoogleEventDateTime parses one end of a Google Calendar event's
// start/end, which is either a timed RFC3339 DateTime or an all-day Date
// ("2006-01-02" with no time component). All-day dates are normalized to
// local midnight, matching how the CalDAV path treats VALUE=DATE all-day
// events (see flattenEvents in caldav.go), for consistent kiosk rendering
// regardless of which provider an event came from.
func parseGoogleEventDateTime(dt *googleapi.EventDateTime) (t time.Time, allDay bool, ok bool) {
	if dt == nil {
		return time.Time{}, false, false
	}
	if dt.DateTime != "" {
		parsed, err := time.Parse(time.RFC3339, dt.DateTime)
		if err != nil {
			return time.Time{}, false, false
		}
		return parsed.In(time.Local), false, true
	}
	if dt.Date != "" {
		parsed, err := time.ParseInLocation("2006-01-02", dt.Date, time.Local)
		if err != nil {
			return time.Time{}, false, false
		}
		return parsed, true, true
	}
	return time.Time{}, false, false
}
