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
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/plugins"
)

// pluginRegistrationRetryInterval controls how often hhq retries
// self-registering with a plugin that wasn't reachable yet (see
// retryPluginRegistration) - short enough that a plugin starting a few
// seconds after hhq (a common ordering in Kubernetes, with no init-container
// dependency between them) is picked up quickly.
// var (not const) so tests can shrink it rather than waiting out the real
// 15 seconds - see retryPluginRegistration's test in plugin_bootstrap_test.go.
var pluginRegistrationRetryInterval = 15 * time.Second

// BootstrapPlugins reconciles the CONFIG_DIR/plugins.json bootstrap file
// against the database on every startup, mirroring
// BootstrapCalendarAccounts's shape (see internal/handlers/sync.go):
// plugins not yet present (matched by id, the stable slug also used as the
// URL path segment) are created, and plugins already present that were
// themselves created by bootstrap are updated to match every time - the
// config file is the source of truth for them, which is also why there's no
// dashboard "add plugin" UI yet. A collision with a manually-added plugin
// (were one ever added outside this file) is skipped rather than
// overwritten. Any existing BootstrapManaged plugin whose id no longer
// appears in entries is deleted (cascades to its synthetic calendar via
// calendar_accounts' ON DELETE CASCADE).
func (a *App) BootstrapPlugins(ctx context.Context, entries []config.PluginBootstrap) {
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		seen[e.ID] = true
		if e.ID == "" || e.BaseURL == "" {
			logging.Errorf("bootstrap: skipping plugins.json entry: id and base_url are both required")
			continue
		}
		name := e.Name
		if name == "" {
			name = e.ID
		}

		existing, err := a.Plugins.GetByID(ctx, e.ID)
		switch {
		case errors.Is(err, models.ErrNotFound):
			if err := a.Plugins.Create(ctx, models.Plugin{
				ID:               e.ID,
				Name:             name,
				BaseURL:          e.BaseURL,
				Enabled:          e.Enabled,
				BootstrapManaged: true,
			}); err != nil {
				logging.Errorf("bootstrap: creating plugin %q: %v", e.ID, err)
				continue
			}
			logging.Infof("bootstrap: registered plugin %q (%s)", e.ID, e.BaseURL)
		case err != nil:
			logging.Errorf("bootstrap: looking up plugin %q: %v", e.ID, err)
			continue
		case !existing.BootstrapManaged:
			logging.Warnf("bootstrap: skipping %q - a plugin with this id already exists and wasn't created via bootstrap", e.ID)
			continue
		default:
			if err := a.Plugins.UpdateBootstrap(ctx, e.ID, name, e.BaseURL, e.Enabled); err != nil {
				logging.Errorf("bootstrap: updating plugin %q: %v", e.ID, err)
				continue
			}
			logging.Debugf("bootstrap: refreshed plugin %q", e.ID)
		}

		if e.Enabled {
			a.ensurePluginReady(ctx, e.ID, e.BaseURL)
		}
	}

	all, err := a.Plugins.ListAll(ctx)
	if err != nil {
		logging.Errorf("bootstrap: listing plugins for removal reconciliation: %v", err)
		return
	}
	for _, p := range all {
		if !p.BootstrapManaged || seen[p.ID] {
			continue
		}
		if err := a.Plugins.Delete(ctx, p.ID); err != nil {
			logging.Errorf("bootstrap: removing plugin %q no longer in plugins.json: %v", p.ID, err)
			continue
		}
		logging.Infof("bootstrap: removed plugin %q - no longer in plugins.json", p.ID)
	}
}

// pluginCalendarCleanupGracePeriod is how long after startup hhq waits before
// checking for orphaned plugin-managed calendar accounts (see
// CleanupOrphanedPluginCalendars) - long enough that ensurePluginReady's 15s
// registration retry (pluginRegistrationRetryInterval) has had several real
// chances to succeed even if hhq and a slow-starting plugin came up at the
// same instant, short enough that a genuinely-removed plugin's synthetic
// calendar and its cached events don't linger indefinitely. var (not const)
// so tests can shrink it rather than waiting out the real default.
var pluginCalendarCleanupGracePeriod = 3 * time.Minute

// SchedulePluginCalendarCleanup runs CleanupOrphanedPluginCalendars exactly
// once, pluginCalendarCleanupGracePeriod after being called, then returns -
// it does not repeat. Meant to be called once from main.go right after the
// bootstrap files (including plugins.json) have been reconciled. ctx is the
// app's shutdown-aware context, so a pending check is abandoned cleanly if
// the process shuts down before the grace period elapses.
func (a *App) SchedulePluginCalendarCleanup(ctx context.Context) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pluginCalendarCleanupGracePeriod):
		}
		a.CleanupOrphanedPluginCalendars(ctx)
	}()
}

// CleanupOrphanedPluginCalendars removes any ProviderPlugin, BootstrapManaged
// calendar account whose owning plugin either no longer exists (e.g. it was
// removed from plugins.json - BootstrapPlugins' own removal loop deletes the
// plugin row, but NOT its synthetic calendar_accounts row: hhq_plugins.
// calendar_id is ON DELETE SET NULL, not the other way around, so nothing
// else ever cleans this up on its own) or has never once connected
// successfully (LastHealthyAt unset - covers both a plugin that's still
// failing to register and one whose row is simply gone). Deleting the
// account cascades to its synthetic calendar and cached events, same as the
// dashboard's own delete button.
//
// This is deliberately NOT run immediately at startup (see
// BootstrapCalendarAccounts, which used to delete these accounts on every
// boot before a plugin had any chance to register - the bug this function
// replaces) - it's meant to run once, well after boot, via
// SchedulePluginCalendarCleanup. A plugin that only manages to connect after
// this check has already removed its account isn't stuck either: the next
// successful manifest fetch re-provisions a fresh synthetic calendar via
// ensurePluginCalendar, just with a new id/color and an empty events cache
// that repopulates on its next sync.
func (a *App) CleanupOrphanedPluginCalendars(ctx context.Context) {
	accounts, err := a.CalendarAccounts.ListAll(ctx)
	if err != nil {
		logging.Errorf("plugin calendar cleanup: listing calendar accounts: %v", err)
		return
	}

	for _, account := range accounts {
		if account.Provider != models.ProviderPlugin || !account.BootstrapManaged {
			continue
		}

		cals, err := a.Calendars.ListForAccount(ctx, account.ID)
		if err != nil {
			logging.Errorf("plugin calendar cleanup: listing calendars for account %q (id=%d): %v", account.Name, account.ID, err)
			continue
		}
		if len(cals) == 0 {
			// Nothing provisioned yet to resolve a plugin from - this
			// shouldn't normally happen (ensurePluginCalendar creates the
			// account and its calendar together), so leave it rather than
			// guess.
			continue
		}

		plugin, err := a.Plugins.GetByCalendarID(ctx, cals[0].ID)
		if err == nil && plugin.LastHealthyAt.Valid {
			continue // owning plugin exists and has connected at least once
		}

		if err != nil {
			logging.Infof("plugin calendar cleanup: removing calendar account %q (id=%d) - owning plugin no longer exists", account.Name, account.ID)
		} else {
			logging.Infof("plugin calendar cleanup: removing calendar account %q (id=%d) - plugin %q never connected within %s of startup", account.Name, account.ID, plugin.ID, pluginCalendarCleanupGracePeriod)
		}
		if err := a.CalendarAccounts.Delete(ctx, account.ID); err != nil {
			logging.Errorf("plugin calendar cleanup: removing calendar account %q (id=%d): %v", account.Name, account.ID, err)
		}
	}
}

// ensurePluginReady makes one immediate attempt to get id ready to talk to
// (self-registered, with its manifest cached) and, if that fails because the
// plugin isn't reachable yet, keeps retrying in the background every
// pluginRegistrationRetryInterval until it succeeds - startup ordering
// between hhq and its plugins isn't guaranteed (e.g. in Kubernetes, with no
// init-container dependency between them), so a plugin that comes up a few
// seconds after hhq shouldn't require a restart to be picked up. Doesn't
// block the caller beyond the first attempt.
func (a *App) ensurePluginReady(ctx context.Context, id, baseURL string) {
	if a.tryRegisterAndRefresh(ctx, id, baseURL) {
		return
	}
	go a.retryPluginRegistration(ctx, id, baseURL)
}

// retryPluginRegistration retries tryRegisterAndRefresh on a ticker until it
// succeeds or ctx is cancelled (app shutdown) - see ensurePluginReady.
func (a *App) retryPluginRegistration(ctx context.Context, id, baseURL string) {
	ticker := time.NewTicker(pluginRegistrationRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logging.Debugf("plugin %q: retrying registration", id)
			if a.tryRegisterAndRefresh(ctx, id, baseURL) {
				return
			}
		}
	}
}

// tryRegisterAndRefresh ensures id has a stored shared token - self-
// registering via the plugin's own POST /register if it doesn't have one
// yet (see internal/plugins/register.go) - then refreshes its cached
// manifest. Returns true only if both steps succeeded, meaning the caller
// should stop retrying. A manifest-fetch failure after a successful
// registration DOES trigger a retry here (via retryPluginRegistration's
// ticker) rather than waiting for scheduler.runPluginSync's own periodic
// tick, since a plugin that registered but isn't fully up yet (e.g. still
// initializing its manifest/view) shouldn't require up to a full sync
// interval to be picked up.
func (a *App) tryRegisterAndRefresh(ctx context.Context, id, baseURL string) bool {
	p, err := a.Plugins.GetByID(ctx, id)
	if err != nil {
		logging.Errorf("plugin %q: looking up before registration: %v", id, err)
		return false
	}

	var token string
	if len(p.EncryptedToken) > 0 {
		token, err = a.Encryptor.Decrypt(p.EncryptedToken)
		if err != nil {
			logging.Errorf("plugin %q: decrypting stored token: %v", id, err)
			return true // not a transient error - retrying won't help
		}
	} else {
		token, err = plugins.Register(ctx, baseURL)
		if err != nil {
			logging.Warnf("plugin %q: not yet reachable to self-register (will retry in %s): %v", id, pluginRegistrationRetryInterval, err)
			return false
		}
		encryptedToken, err := a.Encryptor.Encrypt(token)
		if err != nil {
			logging.Errorf("plugin %q: encrypting received token: %v", id, err)
			return false
		}
		if err := a.Plugins.SetToken(ctx, id, encryptedToken); err != nil {
			logging.Errorf("plugin %q: storing received token: %v", id, err)
			return false
		}
		logging.Infof("plugin %q: self-registered successfully", id)
	}

	return a.refreshPluginManifest(ctx, id, baseURL, token)
}

// refreshPluginManifest fetches a plugin's current GET /manifest and caches
// its nav/view shape/provides_events flag, provisioning a dedicated synthetic
// calendar the first time a provides_events plugin is seen. Tolerant of
// failure (e.g. plugin not up yet) - logged and recorded via MarkHealth
// rather than blocking startup. Returns whether the fetch+cache succeeded,
// so callers (tryRegisterAndRefresh) know whether to keep retrying.
func (a *App) refreshPluginManifest(ctx context.Context, id, baseURL, token string) bool {
	manifest, err := plugins.FetchManifest(ctx, baseURL, token)
	if err != nil {
		logging.Warnf("plugin %q: fetching manifest failed (will retry in %s): %v", id, pluginRegistrationRetryInterval, err)
		_ = a.Plugins.MarkHealth(ctx, id, err)
		return false
	}

	viewLabel, viewIcon := nullableViewSpec(manifest)
	version := sql.NullString{String: manifest.Version, Valid: manifest.Version != ""}
	if err := a.Plugins.UpdateManifest(ctx, id, manifest.View.Enabled, viewLabel, viewIcon, manifest.ProvidesEvents, version); err != nil {
		logging.Errorf("plugin %q: caching manifest: %v", id, err)
		return false
	}
	_ = a.Plugins.MarkHealth(ctx, id, nil)
	logging.Infof("plugin %q: manifest refreshed (view_enabled=%v, provides_events=%v)", id, manifest.View.Enabled, manifest.ProvidesEvents)

	if manifest.ProvidesEvents {
		if err := a.ensurePluginCalendar(ctx, id, manifest.Name); err != nil {
			logging.Errorf("plugin %q: provisioning synthetic calendar: %v", id, err)
			return false
		}
	}
	return true
}

// ensurePluginCalendar auto-creates a dedicated calendar_accounts/calendars
// pair for a provides_events plugin the first time it's seen, and records
// the resulting calendar id back onto the plugins row. This calendar is
// never touched by the real CalDAV sync loop (see
// internal/scheduler.syncAllAccounts's models.ProviderPlugin skip case) -
// its events are synced separately by internal/scheduler's runPluginSync,
// which is also the only thing that ever calls PruneStale on it, so a real
// CalDAV account's sync pass can never delete a plugin's synthetic events.
func (a *App) ensurePluginCalendar(ctx context.Context, id, displayName string) error {
	plugin, err := a.Plugins.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if plugin.CalendarID.Valid {
		return nil // already provisioned
	}

	accountID, err := a.CalendarAccounts.Create(ctx, models.CalendarAccount{
		Name:             fmt.Sprintf("Plugin: %s", displayName),
		Provider:         models.ProviderPlugin,
		BootstrapManaged: true,
	})
	if err != nil {
		return fmt.Errorf("creating synthetic calendar account: %w", err)
	}

	color, err := a.Calendars.NextAvailableColor(ctx)
	if err != nil {
		return fmt.Errorf("picking synthetic calendar color: %w", err)
	}
	calendarID, err := a.Calendars.UpsertDiscovered(ctx, accountID, id, displayName, color)
	if err != nil {
		return fmt.Errorf("creating synthetic calendar: %w", err)
	}

	if err := a.Plugins.SetCalendarID(ctx, id, calendarID); err != nil {
		return fmt.Errorf("recording synthetic calendar id: %w", err)
	}
	logging.Infof("plugin %q: provisioned synthetic calendar id=%d", id, calendarID)
	return nil
}

// nullableViewSpec converts a fetched Manifest's view fields into the
// nullable columns hhq stores - a plugin that opts out of a view
// (View.Enabled == false) gets NULL/NULL.
func nullableViewSpec(m *plugins.Manifest) (sql.NullString, sql.NullString) {
	if !m.View.Enabled {
		return sql.NullString{}, sql.NullString{}
	}
	return sql.NullString{String: m.View.Label, Valid: true},
		sql.NullString{String: m.View.Icon, Valid: true}
}
