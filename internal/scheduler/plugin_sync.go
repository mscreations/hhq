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
		ConnectionSecret: s.Cfg.PluginConnectionSecret,
	}
	for _, p := range list {
		if err := sc.SyncOne(ctx, p, s.Cfg.CalendarWindowDays); err != nil {
			logging.Errorf("scheduler: syncing plugin %q failed: %v", p.ID, err)
		} else {
			logging.Debugf("scheduler: plugin %q synced successfully", p.ID)
		}
	}
}

// runPluginVersionCheck periodically asks every registered, enabled plugin
// for its own GET /version (unauthenticated - the plugin reports whether an
// upgrade is available itself, hhq no longer needs to know its repo or talk
// to GitHub on its behalf), mirroring runReleaseCheck's shape (check once
// immediately on startup, then on a ticker) - reuses s.Cfg.ReleaseCheckInterval
// rather than a separate config knob, since this is the same kind of cheap,
// infrequent poll as hhq's own self-update check.
func (s *Scheduler) runPluginVersionCheck(ctx context.Context) {
	s.checkPluginVersions(ctx)
	ticker := time.NewTicker(s.Cfg.ReleaseCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkPluginVersions(ctx)
		}
	}
}

// checkPluginVersions fetches each enabled plugin's GET /version and caches
// the result in s.PluginVersions for the parent dashboard to read (see
// internal/handlers/parent.go's buildPluginRows). A plugin that fails to
// respond (not yet up, network error, etc.) is skipped for this tick,
// leaving its previously cached value (if any) in place rather than
// clearing it.
func (s *Scheduler) checkPluginVersions(ctx context.Context) {
	list, err := s.Plugins.ListAll(ctx)
	if err != nil {
		logging.Errorf("scheduler: listing plugins for version check: %v", err)
		return
	}

	for _, p := range list {
		if !p.Enabled {
			continue
		}
		info, err := plugins.FetchVersion(ctx, p.BaseURL)
		if err != nil {
			logging.Debugf("scheduler: checking plugin %q version: %v", p.ID, err)
			continue
		}
		s.PluginVersions.Set(p.ID, info)
		logging.Debugf("scheduler: plugin %q reports version %s (upgradeAvailable=%v)", p.ID, info.Version, info.UpgradeAvailable)
	}
}
