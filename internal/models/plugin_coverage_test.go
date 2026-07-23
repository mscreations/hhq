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
	"testing"

	"github.com/mscreations/hhq/internal/testutil"
)

// newTestPluginCalendar creates a bare calendar_accounts/calendars pair to
// satisfy hhq_plugins.calendar_id's FK, mirroring how a real provides_events
// plugin gets its dedicated synthetic calendar (see internal/handlers/
// plugin_bootstrap.go's ensurePluginCalendar) - not itself under test here.
func newTestPluginCalendar(t *testing.T, conn *sql.DB) int {
	t.Helper()
	accounts := &CalendarAccountStore{DB: conn}
	calendars := &CalendarStore{DB: conn}
	ctx := t.Context()

	accountID, err := accounts.Create(ctx, CalendarAccount{
		Name:     "Plugin Synthetic Account",
		Provider: ProviderPlugin,
	})
	if err != nil {
		t.Fatalf("creating synthetic plugin account: %v", err)
	}
	calID, err := calendars.UpsertDiscovered(ctx, accountID, "/plugin/", "Plugin Calendar", "#3B82F6")
	if err != nil {
		t.Fatalf("creating synthetic plugin calendar: %v", err)
	}
	return calID
}

func TestPluginStoreCreateAndGetByID(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}
	ctx := t.Context()

	if err := s.Create(ctx, Plugin{ID: "billtracker", Name: "Bill Tracker", BaseURL: "http://billtracker:8090", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.GetByID(ctx, "billtracker")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Bill Tracker" || got.BaseURL != "http://billtracker:8090" || !got.Enabled {
		t.Fatalf("unexpected plugin: %+v", got)
	}
	if got.BootstrapManaged {
		t.Fatal("expected BootstrapManaged to default to false")
	}
}

func TestPluginStoreCreatePersistsBootstrapManaged(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}
	ctx := t.Context()

	if err := s.Create(ctx, Plugin{ID: "bootstrap-plugin", Name: "Bootstrap Plugin", BaseURL: "http://x:1", Enabled: true, BootstrapManaged: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.GetByID(ctx, "bootstrap-plugin")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.BootstrapManaged {
		t.Fatal("expected BootstrapManaged to persist as true")
	}
}

func TestPluginStoreGetByIDNotFound(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}

	if _, err := s.GetByID(t.Context(), "nonexistent"); err != ErrNotFound {
		t.Fatalf("GetByID missing: err = %v, want ErrNotFound", err)
	}
}

func TestPluginStoreListAll(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}
	ctx := t.Context()

	if err := s.Create(ctx, Plugin{ID: "zebra", Name: "Zebra Plugin", BaseURL: "http://z:1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Create(ctx, Plugin{ID: "alpha", Name: "Alpha Plugin", BaseURL: "http://a:1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	list, err := s.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(list) != 2 || list[0].Name != "Alpha Plugin" || list[1].Name != "Zebra Plugin" {
		t.Fatalf("ListAll = %+v, want Alpha then Zebra (ORDER BY name)", list)
	}
}

// TestPluginStoreListEnabledFiltersOnEnabledProvidesEventsAndCalendarID
// covers ListEnabled's three-way filter (all must hold: enabled, provides
// events, and a synthetic calendar already provisioned).
func TestPluginStoreListEnabledFiltersOnEnabledProvidesEventsAndCalendarID(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}
	ctx := t.Context()

	calID := newTestPluginCalendar(t, conn)

	if err := s.Create(ctx, Plugin{ID: "ready", Name: "Ready Plugin", BaseURL: "http://x:1", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.UpdateManifest(ctx, "ready", false, sql.NullString{}, sql.NullString{}, true, sql.NullString{}); err != nil {
		t.Fatalf("UpdateManifest: %v", err)
	}
	if err := s.SetCalendarID(ctx, "ready", calID); err != nil {
		t.Fatalf("SetCalendarID: %v", err)
	}

	if err := s.Create(ctx, Plugin{ID: "disabled", Name: "Disabled Plugin", BaseURL: "http://x:2", Enabled: false}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.UpdateManifest(ctx, "disabled", false, sql.NullString{}, sql.NullString{}, true, sql.NullString{}); err != nil {
		t.Fatalf("UpdateManifest: %v", err)
	}
	if err := s.SetCalendarID(ctx, "disabled", calID); err != nil {
		t.Fatalf("SetCalendarID: %v", err)
	}

	if err := s.Create(ctx, Plugin{ID: "no-events", Name: "No Events Plugin", BaseURL: "http://x:3", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// provides_events left false, no calendar assigned.

	list, err := s.ListEnabled(ctx)
	if err != nil {
		t.Fatalf("ListEnabled: %v", err)
	}
	if len(list) != 1 || list[0].ID != "ready" {
		t.Fatalf("ListEnabled = %+v, want only the fully-ready plugin", list)
	}
}

func TestPluginStoreListViewsFiltersOnEnabledAndViewEnabled(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}
	ctx := t.Context()

	if err := s.Create(ctx, Plugin{ID: "viewable", Name: "Viewable Plugin", BaseURL: "http://x:1", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.UpdateManifest(ctx, "viewable", true, sql.NullString{String: "Bills", Valid: true}, sql.NullString{String: "dollar", Valid: true}, false, sql.NullString{}); err != nil {
		t.Fatalf("UpdateManifest: %v", err)
	}

	if err := s.Create(ctx, Plugin{ID: "not-viewable", Name: "Not Viewable Plugin", BaseURL: "http://x:2", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// view_enabled left false.

	list, err := s.ListViews(ctx)
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	if len(list) != 1 || list[0].ID != "viewable" {
		t.Fatalf("ListViews = %+v, want only the view-enabled plugin", list)
	}
	if list[0].ViewLabel.String != "Bills" || list[0].ViewIcon.String != "dollar" {
		t.Fatalf("unexpected view metadata: %+v", list[0])
	}
}

func TestPluginStoreGetByCalendarID(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}
	ctx := t.Context()

	calID := newTestPluginCalendar(t, conn)
	if err := s.Create(ctx, Plugin{ID: "billtracker", Name: "Bill Tracker", BaseURL: "http://x:1", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.SetCalendarID(ctx, "billtracker", calID); err != nil {
		t.Fatalf("SetCalendarID: %v", err)
	}

	got, err := s.GetByCalendarID(ctx, calID)
	if err != nil {
		t.Fatalf("GetByCalendarID: %v", err)
	}
	if got.ID != "billtracker" {
		t.Fatalf("GetByCalendarID = %+v, want billtracker", got)
	}
}

func TestPluginStoreGetByCalendarIDNotFound(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}

	if _, err := s.GetByCalendarID(t.Context(), 99999); err != ErrNotFound {
		t.Fatalf("GetByCalendarID for unassigned calendar: err = %v, want ErrNotFound", err)
	}
}

func TestPluginStoreUpdateBootstrap(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}
	ctx := t.Context()

	if err := s.Create(ctx, Plugin{ID: "billtracker", Name: "Bill Tracker", BaseURL: "http://old:1", Enabled: true, BootstrapManaged: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.UpdateBootstrap(ctx, "billtracker", "Bill Tracker Renamed", "http://new:2", false); err != nil {
		t.Fatalf("UpdateBootstrap: %v", err)
	}

	got, err := s.GetByID(ctx, "billtracker")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Bill Tracker Renamed" || got.BaseURL != "http://new:2" || got.Enabled {
		t.Fatalf("UpdateBootstrap did not apply: %+v", got)
	}
}

func TestPluginStoreSetToken(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}
	ctx := t.Context()

	if err := s.Create(ctx, Plugin{ID: "billtracker", Name: "Bill Tracker", BaseURL: "http://x:1", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.GetByID(ctx, "billtracker")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.EncryptedToken != nil {
		t.Fatalf("expected no token before registration, got %v", got.EncryptedToken)
	}

	if err := s.SetToken(ctx, "billtracker", []byte("encrypted-token")); err != nil {
		t.Fatalf("SetToken: %v", err)
	}

	got, err = s.GetByID(ctx, "billtracker")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if string(got.EncryptedToken) != "encrypted-token" {
		t.Fatalf("SetToken did not persist: %+v", got)
	}
}

func TestPluginStoreSetEnabled(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}
	ctx := t.Context()

	if err := s.Create(ctx, Plugin{ID: "billtracker", Name: "Bill Tracker", BaseURL: "http://x:1", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.SetEnabled(ctx, "billtracker", false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	got, err := s.GetByID(ctx, "billtracker")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Enabled {
		t.Fatal("expected Enabled to be false")
	}

	if err := s.SetEnabled(ctx, "billtracker", true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	got, err = s.GetByID(ctx, "billtracker")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.Enabled {
		t.Fatal("expected Enabled to be true")
	}
}

// TestPluginStoreMarkHealth covers both branches: a nil syncErr sets
// last_healthy_at and clears last_error, a non-nil syncErr records the error
// without touching (or clearing) the previously-recorded healthy timestamp.
func TestPluginStoreMarkHealth(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}
	ctx := t.Context()

	if err := s.Create(ctx, Plugin{ID: "billtracker", Name: "Bill Tracker", BaseURL: "http://x:1", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.MarkHealth(ctx, "billtracker", nil); err != nil {
		t.Fatalf("MarkHealth(nil): %v", err)
	}
	got, err := s.GetByID(ctx, "billtracker")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.LastHealthyAt.Valid || got.LastError.Valid {
		t.Fatalf("expected healthy timestamp set and no error, got %+v", got)
	}
	healthyAt := got.LastHealthyAt.Time

	if err := s.MarkHealth(ctx, "billtracker", sql.ErrConnDone); err != nil {
		t.Fatalf("MarkHealth(err): %v", err)
	}
	got, err = s.GetByID(ctx, "billtracker")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !got.LastError.Valid || got.LastError.String != sql.ErrConnDone.Error() {
		t.Fatalf("expected sync error recorded, got %+v", got.LastError)
	}
	if !got.LastHealthyAt.Time.Equal(healthyAt) {
		t.Fatalf("expected last_healthy_at to be left untouched by a failed health check, before=%v after=%v", healthyAt, got.LastHealthyAt.Time)
	}
}

func TestPluginStoreDelete(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}
	ctx := t.Context()

	if err := s.Create(ctx, Plugin{ID: "billtracker", Name: "Bill Tracker", BaseURL: "http://x:1", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Delete(ctx, "billtracker"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := s.GetByID(ctx, "billtracker"); err != ErrNotFound {
		t.Fatalf("GetByID after delete: err = %v, want ErrNotFound", err)
	}
}

// TestPluginQueriesReturnErrorOnCanceledContext covers the query-error return
// branches in scanPlugins/scanPlugin's callers, otherwise unreachable
// without breaking the DB connection - see calendar_coverage_test.go's
// equivalent test for the same technique/rationale.
func TestPluginQueriesReturnErrorOnCanceledContext(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &PluginStore{DB: conn}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := s.ListAll(ctx); err == nil {
		t.Error("ListAll: expected error on canceled context")
	}
	if _, err := s.ListEnabled(ctx); err == nil {
		t.Error("ListEnabled: expected error on canceled context")
	}
	if _, err := s.ListViews(ctx); err == nil {
		t.Error("ListViews: expected error on canceled context")
	}
	if _, err := s.GetByID(ctx, "x"); err == nil {
		t.Error("GetByID: expected error on canceled context")
	}
	if _, err := s.GetByCalendarID(ctx, 1); err == nil {
		t.Error("GetByCalendarID: expected error on canceled context")
	}
}
