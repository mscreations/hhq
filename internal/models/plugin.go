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

package models

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Plugin is one registered external-process plugin (see internal/plugins) -
// its reachability (BaseURL), and, if ProvidesEvents, the dedicated
// synthetic Calendar its events are upserted into (see
// internal/scheduler/plugin_sync.go). Its kiosk nav buttons/views are cached
// separately, in hhq_plugin_views (see PluginView/ReplaceViews/ListViews) -
// a plugin can register any number of them, not just one.
type Plugin struct {
	ID               string
	Name             string
	BaseURL          string
	Enabled          bool
	ProvidesEvents   bool
	CalendarID       sql.NullInt32
	EncryptedToken   []byte
	BootstrapManaged bool
	Version          sql.NullString
	LastHealthyAt    sql.NullTime
	LastError        sql.NullString
	CreatedAt        time.Time
}

// PluginView is one kiosk nav button/full-screen view a plugin has
// registered (see hhq_plugin_views, added by migration 00002). Cached from
// the plugin's last successful GET /manifest fetch (Manifest.Views) -
// PluginStore.ReplaceViews replaces a plugin's entire set on every refresh,
// so there's no separate "enabled" flag to track here: a view not currently
// in the plugin's manifest simply isn't in this table. PluginID is only
// populated when returned by ListViews (its join needs it, to attribute
// each view back to its plugin for the kiosk nav bar) - callers building a
// []PluginView to pass into ReplaceViews don't need to set it, since
// ReplaceViews takes the plugin id as its own separate argument.
type PluginView struct {
	PluginID string
	ViewID   string
	Label    string
	Icon     string
}

type PluginStore struct {
	DB *sql.DB
}

const pluginColumns = `id, name, base_url, enabled, provides_events, calendar_id, encrypted_token, bootstrap_managed, version, last_healthy_at, last_error, created_at`

func (s *PluginStore) ListAll(ctx context.Context) ([]Plugin, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+pluginColumns+` FROM hhq_plugins ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlugins(rows)
}

// ListEnabled returns enabled plugins that provide synthetic events and have
// a synthetic calendar already provisioned - the set internal/scheduler's
// runPluginSync iterates every tick.
func (s *PluginStore) ListEnabled(ctx context.Context) ([]Plugin, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT `+pluginColumns+` FROM hhq_plugins
		WHERE enabled = TRUE AND provides_events = TRUE AND calendar_id IS NOT NULL
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlugins(rows)
}

// ListViews returns every kiosk nav button/full-screen view belonging to an
// enabled plugin - one row per (plugin, view) pair, ordered by plugin name
// then by the view's sort_order (the order the plugin's manifest listed
// them in - see ReplaceViews). A plugin with zero registered views (or that
// isn't enabled) contributes no rows.
func (s *PluginStore) ListViews(ctx context.Context) ([]PluginView, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT pv.plugin_id, pv.view_id, pv.label, pv.icon
		FROM hhq_plugin_views pv
		JOIN hhq_plugins p ON p.id = pv.plugin_id
		WHERE p.enabled = TRUE
		ORDER BY p.name, pv.sort_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PluginView
	for rows.Next() {
		var v PluginView
		var icon sql.NullString
		if err := rows.Scan(&v.PluginID, &v.ViewID, &v.Label, &icon); err != nil {
			return nil, err
		}
		v.Icon = icon.String
		out = append(out, v)
	}
	return out, rows.Err()
}

// ReplaceViews replaces pluginID's entire set of registered views with
// views, in the order given (persisted as sort_order) - called after every
// successful GET /manifest fetch (see internal/handlers/plugin_bootstrap.go's
// refreshPluginManifest), so a view a plugin has stopped advertising is
// removed here rather than lingering as a dead nav button.
func (s *PluginStore) ReplaceViews(ctx context.Context, pluginID string, views []PluginView) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM hhq_plugin_views WHERE plugin_id = $1`, pluginID); err != nil {
		return err
	}
	for i, v := range views {
		icon := sql.NullString{String: v.Icon, Valid: v.Icon != ""}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hhq_plugin_views (plugin_id, view_id, label, icon, sort_order)
			VALUES ($1, $2, $3, $4, $5)`, pluginID, v.ViewID, v.Label, icon, i); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *PluginStore) GetByID(ctx context.Context, id string) (*Plugin, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+pluginColumns+` FROM hhq_plugins WHERE id = $1`, id)
	p, err := scanPlugin(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// GetByCalendarID finds the plugin whose synthetic calendar (see
// internal/handlers/plugin_bootstrap.go's ensurePluginCalendar) is
// calendarID - used to resolve a plugin from its owning calendar_accounts
// row's id for the dashboard's on-demand "Resync Now" (internal/handlers/
// sync.go), which only has the calendar_accounts id to start from.
func (s *PluginStore) GetByCalendarID(ctx context.Context, calendarID int) (*Plugin, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT `+pluginColumns+` FROM hhq_plugins WHERE calendar_id = $1`, calendarID)
	p, err := scanPlugin(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *PluginStore) Create(ctx context.Context, p Plugin) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO hhq_plugins (id, name, base_url, enabled, bootstrap_managed)
		VALUES ($1, $2, $3, $4, $5)`,
		p.ID, p.Name, p.BaseURL, p.Enabled, p.BootstrapManaged)
	return err
}

// UpdateBootstrap refreshes a bootstrap-managed plugin's name/base_url/
// enabled to match its current plugins.json entry - mirrors
// CalendarAccountStore.UpdateBootstrap. Deliberately leaves encrypted_token
// untouched - self-registration (see SetToken) is independent of config
// reconciliation.
func (s *PluginStore) UpdateBootstrap(ctx context.Context, id, name, baseURL string, enabled bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_plugins SET name = $2, base_url = $3, enabled = $4 WHERE id = $1`, id, name, baseURL, enabled)
	return err
}

// SetToken records the shared secret a plugin issued via its own POST
// /register (see internal/plugins/register.go and internal/handlers/
// plugin_bootstrap.go's tryRegisterAndRefresh) - called exactly once per
// plugin, the first time it's successfully reachable.
func (s *PluginStore) SetToken(ctx context.Context, id string, encryptedToken []byte) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_plugins SET encrypted_token = $2 WHERE id = $1`, id, encryptedToken)
	return err
}

// UpdateManifest caches the provides_events flag/version from a successful
// GET /manifest fetch (see internal/plugins.FetchManifest). The manifest's
// view list is cached separately via ReplaceViews, not here.
func (s *PluginStore) UpdateManifest(ctx context.Context, id string, providesEvents bool, version sql.NullString) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE hhq_plugins SET provides_events = $2, version = $3
		WHERE id = $1`, id, providesEvents, version)
	return err
}

// SetCalendarID records the dedicated synthetic calendar auto-created for a
// provides_events plugin (see internal/handlers/plugin_bootstrap.go).
func (s *PluginStore) SetCalendarID(ctx context.Context, id string, calendarID int) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_plugins SET calendar_id = $2 WHERE id = $1`, id, calendarID)
	return err
}

func (s *PluginStore) SetEnabled(ctx context.Context, id string, enabled bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_plugins SET enabled = $2 WHERE id = $1`, id, enabled)
	return err
}

// MarkHealth records the result of the most recent contact with the plugin
// (a /manifest, /view, or /events fetch) - nil syncErr updates
// last_healthy_at, a non-nil one records the error without touching
// last_healthy_at (so the dashboard can show both "last seen healthy" and
// "current error" at once).
func (s *PluginStore) MarkHealth(ctx context.Context, id string, syncErr error) error {
	if syncErr != nil {
		_, err := s.DB.ExecContext(ctx, `UPDATE hhq_plugins SET last_error = $2 WHERE id = $1`, id, syncErr.Error())
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE hhq_plugins SET last_healthy_at = now(), last_error = NULL WHERE id = $1`, id)
	return err
}

func (s *PluginStore) Delete(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM hhq_plugins WHERE id = $1`, id)
	return err
}

func scanPlugins(rows *sql.Rows) ([]Plugin, error) {
	var out []Plugin
	for rows.Next() {
		var p Plugin
		if err := rows.Scan(&p.ID, &p.Name, &p.BaseURL, &p.Enabled, &p.ProvidesEvents, &p.CalendarID, &p.EncryptedToken, &p.BootstrapManaged, &p.Version, &p.LastHealthyAt, &p.LastError, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanPlugin(row rowScanner) (*Plugin, error) {
	var p Plugin
	if err := row.Scan(&p.ID, &p.Name, &p.BaseURL, &p.Enabled, &p.ProvidesEvents, &p.CalendarID, &p.EncryptedToken, &p.BootstrapManaged, &p.Version, &p.LastHealthyAt, &p.LastError, &p.CreatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}
