package plugins

import (
	"context"
	"database/sql"
	"time"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/util"
)

// SyncContext bundles the stores SyncOne needs to fetch one plugin's
// synthetic events and record the result - shared by the scheduler's
// periodic pass (internal/scheduler/plugin_sync.go) and the parent
// dashboard's on-demand "Resync Now" (internal/handlers/sync.go), so both
// paths sync a plugin identically and report the same health/last-synced
// status regardless of which one triggered it.
type SyncContext struct {
	Plugins          *models.PluginStore
	Calendars        *models.CalendarStore
	Events           *models.EventStore
	CalendarAccounts *models.CalendarAccountStore
	Encryptor        *util.Encryptor
}

// SyncOne fetches p's synthetic events for [today, today+windowDays) and
// upserts them into its dedicated synthetic calendar (see
// internal/handlers/plugin_bootstrap.go's ensurePluginCalendar), pruning any
// previously-synced events the plugin no longer returns. Marks the plugin's
// health, the synthetic calendar's sync status, and (if resolvable) the
// synthetic calendar's owning calendar_accounts row's sync status - mirrors
// the shape of internal/scheduler/plugin_sync.go's syncAllPlugins loop body,
// just for a single plugin instead of every enabled one.
func (sc SyncContext) SyncOne(ctx context.Context, p models.Plugin, windowDays int) error {
	calendarID := int(p.CalendarID.Int32)

	accountID := 0
	if cal, err := sc.Calendars.GetByID(ctx, calendarID); err == nil {
		accountID = cal.CalendarAccountID
	}

	from := time.Now()
	to := from.AddDate(0, 0, windowDays)

	token, err := sc.Encryptor.Decrypt(p.EncryptedToken)
	if err != nil {
		_ = sc.Plugins.MarkHealth(ctx, p.ID, err)
		_ = sc.Calendars.MarkSynced(ctx, calendarID, err)
		if accountID != 0 {
			_ = sc.CalendarAccounts.MarkSynced(ctx, accountID, err)
		}
		return err
	}

	events, err := FetchEvents(ctx, p.BaseURL, token, from, to)
	if err != nil {
		_ = sc.Plugins.MarkHealth(ctx, p.ID, err)
		_ = sc.Calendars.MarkSynced(ctx, calendarID, err)
		if accountID != 0 {
			_ = sc.CalendarAccounts.MarkSynced(ctx, accountID, err)
		}
		return err
	}

	seenUIDs := make([]string, 0, len(events))
	for _, e := range events {
		if uerr := sc.Events.Upsert(ctx, models.Event{
			CalendarID:  calendarID,
			UID:         e.UID,
			Summary:     e.Summary,
			Location:    nullableString(e.Location),
			Description: nullableString(e.Description),
			StartsAt:    e.StartsAt,
			EndsAt:      e.EndsAt,
			AllDay:      e.AllDay,
			Actions:     convertActions(e.Actions),
		}); uerr != nil {
			continue
		}
		seenUIDs = append(seenUIDs, e.UID)
	}

	pruneErr := sc.Events.PruneStale(ctx, calendarID, seenUIDs)

	_ = sc.Plugins.MarkHealth(ctx, p.ID, nil)
	_ = sc.Calendars.MarkSynced(ctx, calendarID, nil)
	if accountID != 0 {
		_ = sc.CalendarAccounts.MarkSynced(ctx, accountID, nil)
	}
	return pruneErr
}

func nullableString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// convertActions maps the plugin-protocol EventAction shape onto the
// storage-layer models.EventAction shape - kept as distinct types since one
// is a JSON wire contract (internal/plugins) and the other is a DB storage
// shape (internal/models), even though they currently look identical.
func convertActions(actions []EventAction) []models.EventAction {
	if len(actions) == 0 {
		return nil
	}
	out := make([]models.EventAction, len(actions))
	for i, a := range actions {
		out[i] = models.EventAction{ID: a.ID, Label: a.Label, RequiresParent: a.RequiresParent}
	}
	return out
}
