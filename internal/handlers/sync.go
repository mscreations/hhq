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

package handlers

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mscreations/hhq/internal/caldav"
	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/plugins"
)

// syncStatus tracks which calendar account IDs currently have a background
// sync in flight (see syncAccountAsync below), so the dashboard can render a
// busy state on "Resync Now" and self-poll until it clears. Zero value is
// ready to use.
type syncStatus struct {
	mu  sync.RWMutex
	ids map[int]bool
}

func (s *syncStatus) start(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids == nil {
		s.ids = make(map[int]bool)
	}
	s.ids[id] = true
}

func (s *syncStatus) done(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
}

func (s *syncStatus) isSyncing(id int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ids[id]
}

func (s *syncStatus) any() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.ids) > 0
}

// syncAccountAsync runs a full discovery+sync pass for a single calendar
// account in the background, so a parent adding, editing, or clicking
// "Resync Now" on an account doesn't have to wait up to 15 minutes - or
// restart the app - to see it take effect. Discovery re-runs every time
// (cheap - it's just a calendar listing), which is what picks up newly
// created calendars on the server and refreshes display names; existing
// calendars' color and enabled/disabled choices are preserved (see
// models.CalendarStore.UpsertDiscovered).
func (a *App) syncAccountAsync(accountID int) {
	a.syncing.start(accountID)
	go func() {
		defer a.syncing.done(accountID)

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		logging.Debugf("on-demand sync: starting for account id=%d", accountID)

		account, err := a.CalendarAccounts.GetByID(ctx, accountID)
		if err != nil {
			logging.Errorf("on-demand sync: loading account id=%d: %v", accountID, err)
			return
		}

		if account.Provider == models.ProviderPlugin {
			// Synthetic calendar accounts auto-created for plugins have no
			// password, so they can't go through the CalDAV path below -
			// resolve the owning plugin via its synthetic calendar and sync
			// it directly instead, using the same plugins.SyncOne the
			// scheduler's periodic pass uses (internal/scheduler/
			// plugin_sync.go), so "Resync Now" actually does something for
			// these accounts instead of silently no-oping.
			a.syncPluginAccount(ctx, account)
			return
		}

		if account.Provider == models.ProviderGoogle {
			// Google accounts authenticate via OAuth2 refresh token, not a
			// password - handled entirely separately from the CalDAV path
			// below, the same way ProviderPlugin is handled above.
			refreshToken, err := a.Encryptor.Decrypt(account.EncryptedRefreshToken)
			if err != nil {
				logging.Errorf("on-demand sync: decrypting refresh token for account %q: %v", account.Name, err)
				_ = a.CalendarAccounts.MarkSynced(ctx, account.ID, err)
				return
			}
			syncErr := caldav.SyncGoogleAccount(ctx, a.Calendars, a.Events, *account, refreshToken, config.GoogleOAuthConfig(a.Cfg), a.Cfg.CalendarWindowDays)
			if syncErr != nil {
				logging.Errorf("on-demand sync: account %q failed: %v", account.Name, syncErr)
			} else {
				logging.Infof("on-demand sync: account %q succeeded", account.Name)
			}
			_ = a.CalendarAccounts.MarkSynced(ctx, account.ID, syncErr)
			return
		}

		password, err := a.Encryptor.Decrypt(account.EncryptedPassword)
		if err != nil {
			logging.Errorf("on-demand sync: decrypting password for account %q: %v", account.Name, err)
			_ = a.CalendarAccounts.MarkSynced(ctx, account.ID, err)
			return
		}

		var syncErr error
		switch account.Provider {
		case models.ProviderFastmail, models.ProviderICloud, models.ProviderGeneric:
			syncErr = caldav.DiscoverAndSyncAccount(ctx, a.Calendars, a.Events, *account, password, a.Cfg.CalendarWindowDays)
		default:
			logging.Warnf("on-demand sync: provider %q not yet supported for account %q, skipping", account.Provider, account.Name)
			return
		}

		if syncErr != nil {
			logging.Errorf("on-demand sync: account %q failed: %v", account.Name, syncErr)
		} else {
			logging.Infof("on-demand sync: account %q succeeded", account.Name)
		}
		_ = a.CalendarAccounts.MarkSynced(ctx, account.ID, syncErr)
	}()
}

// syncPluginAccount finds the plugin whose synthetic calendar lives under
// account (a ProviderPlugin calendar_accounts row - see
// internal/handlers/plugin_bootstrap.go's ensurePluginCalendar) and syncs it
// via plugins.SyncOne, the same routine the scheduler's periodic pass uses
// (internal/scheduler/plugin_sync.go). Called from within syncAccountAsync's
// goroutine, so account.ID's syncing state is already tracked by the caller.
func (a *App) syncPluginAccount(ctx context.Context, account *models.CalendarAccount) {
	cals, err := a.Calendars.ListForAccount(ctx, account.ID)
	if err != nil || len(cals) == 0 {
		logging.Errorf("on-demand sync: no synthetic calendar found for plugin account %q (id=%d): %v", account.Name, account.ID, err)
		return
	}

	plugin, err := a.Plugins.GetByCalendarID(ctx, cals[0].ID)
	if err != nil {
		logging.Errorf("on-demand sync: no plugin found for synthetic calendar id=%d (account %q): %v", cals[0].ID, account.Name, err)
		return
	}

	sc := plugins.SyncContext{
		Plugins:          a.Plugins,
		Calendars:        a.Calendars,
		Events:           a.Events,
		CalendarAccounts: a.CalendarAccounts,
		Encryptor:        a.Encryptor,
	}
	if err := sc.SyncOne(ctx, *plugin, a.Cfg.CalendarWindowDays); err != nil {
		logging.Errorf("on-demand sync: plugin %q failed: %v", plugin.ID, err)
	} else {
		logging.Infof("on-demand sync: plugin %q succeeded", plugin.ID)
	}
}

// BootstrapCalendarAccounts reconciles the CONFIG_DIR/calendars.json
// bootstrap file against the database on every startup: accounts not yet present (matched
// by name) are created, and accounts already present that were themselves
// created by bootstrap are updated to match the config every time - the
// config file is the source of truth for these accounts, which is also why
// they're marked BootstrapManaged and refused edits/deletes through the
// parent UI (see CreateCalendarAccount/UpdateCalendarAccount/
// DeleteCalendarAccount). A name collision with an account created through
// the UI is skipped rather than overwritten, since that account's config
// wasn't sourced from this file. Any existing BootstrapManaged account whose
// name no longer appears in entries is deleted (same as the dashboard's
// delete button - cascades to its calendars/cached events), since otherwise
// there would be no way to remove an account added via bootstrap short of
// reaching into the database directly.
func (a *App) BootstrapCalendarAccounts(ctx context.Context, entries []config.CalendarAccountBootstrap) {
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		seen[e.Name] = true
		if e.Name == "" || e.Username == "" || e.Password == "" {
			logging.Errorf("bootstrap: skipping calendar account %q: name, username, and password are all required", e.Name)
			continue
		}
		provider, err := config.ResolveProvider(e.Provider)
		if err != nil {
			logging.Errorf("bootstrap: skipping calendar account %q: %v", e.Name, err)
			continue
		}
		url := models.DefaultCalDAVURL(provider, e.URL)
		if url == "" {
			logging.Errorf("bootstrap: skipping calendar account %q: provider %q requires an explicit url", e.Name, provider)
			continue
		}
		encrypted, err := a.Encryptor.Encrypt(e.Password)
		if err != nil {
			logging.Errorf("bootstrap: encrypting password for calendar account %q: %v", e.Name, err)
			continue
		}

		existing, err := a.CalendarAccounts.GetByName(ctx, e.Name)
		switch {
		case errors.Is(err, models.ErrAccountNotFound):
			id, err := a.CalendarAccounts.Create(ctx, models.CalendarAccount{
				Name:              e.Name,
				Provider:          provider,
				CalDAVURL:         nullString(url),
				Username:          nullString(e.Username),
				EncryptedPassword: encrypted,
				BootstrapManaged:  true,
			})
			if err != nil {
				logging.Errorf("bootstrap: creating calendar account %q: %v", e.Name, err)
				continue
			}
			logging.Infof("bootstrap: created calendar account %q (id=%d, provider=%s)", e.Name, id, provider)
			a.syncAccountAsync(id)
		case err != nil:
			logging.Errorf("bootstrap: looking up calendar account %q: %v", e.Name, err)
		case !existing.BootstrapManaged:
			logging.Warnf("bootstrap: skipping %q - an account with this name already exists and was created via the dashboard, not bootstrap", e.Name)
		default:
			if err := a.CalendarAccounts.UpdateBootstrap(ctx, existing.ID, provider, url, e.Username, encrypted); err != nil {
				logging.Errorf("bootstrap: updating calendar account %q (id=%d): %v", e.Name, existing.ID, err)
				continue
			}
			logging.Debugf("bootstrap: refreshed calendar account %q (id=%d)", e.Name, existing.ID)
			a.syncAccountAsync(existing.ID)
		}
	}

	all, err := a.CalendarAccounts.ListAll(ctx)
	if err != nil {
		logging.Errorf("bootstrap: listing calendar accounts for removal reconciliation: %v", err)
		return
	}
	for _, account := range all {
		if !account.BootstrapManaged || seen[account.Name] {
			continue
		}
		if err := a.CalendarAccounts.Delete(ctx, account.ID); err != nil {
			logging.Errorf("bootstrap: removing calendar account %q (id=%d) no longer in calendars.json: %v", account.Name, account.ID, err)
			continue
		}
		logging.Infof("bootstrap: removed calendar account %q (id=%d) - no longer in calendars.json", account.Name, account.ID)
	}
}
