package scheduler

import (
	"context"
	"time"

	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/plugins"
)

// runPluginSync periodically fetches synthetic calendar events from every
// registered, enabled, events-providing plugin - mirrors runCalendarSync's
// shape (sync once immediately on startup, then on a ticker).
func (s *Scheduler) runPluginSync(ctx context.Context) {
	logging.Debugf("scheduler: running initial plugin sync on startup")
	s.syncAllPlugins(ctx)
	ticker := time.NewTicker(s.Cfg.PluginSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logging.Debugf("scheduler: plugin sync tick")
			s.syncAllPlugins(ctx)
		}
	}
}

// syncAllPlugins fetches each enabled, events-providing plugin's synthetic
// events for the same [today, today+CalendarWindowDays) window used for real
// CalDAV sync, and upserts them into that plugin's dedicated synthetic
// calendar (see internal/handlers/plugin_bootstrap.go's ensurePluginCalendar).
// Isolation from real CalDAV calendars is structural, not a guard here: each
// plugin has its own never-shared calendar_id, so this loop's PruneStale
// call only ever removes that plugin's own stale rows - identical to how two
// independent real CalDAV accounts never interfere with each other today.
func (s *Scheduler) syncAllPlugins(ctx context.Context) {
	list, err := s.Plugins.ListEnabled(ctx)
	if err != nil {
		logging.Errorf("scheduler: listing plugins: %v", err)
		return
	}
	logging.Debugf("scheduler: syncing %d plugin(s) for synthetic events", len(list))

	sc := plugins.SyncContext{
		Plugins:          s.Plugins,
		Calendars:        s.Calendars,
		Events:           s.Events,
		CalendarAccounts: s.CalendarAccounts,
		Encryptor:        s.Encryptor,
	}
	for _, p := range list {
		if err := sc.SyncOne(ctx, p, s.Cfg.CalendarWindowDays); err != nil {
			logging.Errorf("scheduler: syncing plugin %q failed: %v", p.ID, err)
		} else {
			logging.Debugf("scheduler: plugin %q synced successfully", p.ID)
		}
	}
}
