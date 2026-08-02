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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/plugins"
	"github.com/mscreations/hhq/internal/testutil"
)

// newFakePluginVersionServer serves just GET /version, the only endpoint
// checkPluginVersions talks to.
func newFakePluginVersionServer(t *testing.T, info plugins.VersionInfo) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckPluginVersionsCachesUpgradeAvailableResponse(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	srv := newFakePluginVersionServer(t, plugins.VersionInfo{
		Version:          "1.0.0",
		UpgradeAvailable: true,
		UpgradeVersion:   "1.0.2",
		Changelog:        "feat: Update versioning",
		Channel:          "dev",
	})

	pluginStore := &models.PluginStore{DB: conn}
	if err := pluginStore.Create(ctx, models.Plugin{ID: "bill-tracker", Name: "Bill Tracker", BaseURL: srv.URL, Enabled: true, BootstrapManaged: true}); err != nil {
		t.Fatalf("Plugins.Create: %v", err)
	}

	s := &Scheduler{Plugins: pluginStore, PluginVersions: &plugins.VersionCache{}}
	s.checkPluginVersions(ctx)

	got, ok := s.PluginVersions.Get("bill-tracker")
	if !ok {
		t.Fatal("expected a cached version info for bill-tracker")
	}
	if !got.UpgradeAvailable || got.UpgradeVersion != "1.0.2" || got.Changelog != "feat: Update versioning" {
		t.Errorf("got %+v", got)
	}
}

func TestCheckPluginVersionsSkipsDisabledPlugins(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	srv := newFakePluginVersionServer(t, plugins.VersionInfo{Version: "1.0.0", UpgradeAvailable: true, UpgradeVersion: "1.0.2"})

	pluginStore := &models.PluginStore{DB: conn}
	if err := pluginStore.Create(ctx, models.Plugin{ID: "bill-tracker", Name: "Bill Tracker", BaseURL: srv.URL, Enabled: false, BootstrapManaged: true}); err != nil {
		t.Fatalf("Plugins.Create: %v", err)
	}

	s := &Scheduler{Plugins: pluginStore, PluginVersions: &plugins.VersionCache{}}
	s.checkPluginVersions(ctx)

	if _, ok := s.PluginVersions.Get("bill-tracker"); ok {
		t.Fatal("expected a disabled plugin to never be checked")
	}
}

func TestCheckPluginVersionsLeavesPriorCachedValueOnFetchError(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	// A base URL that refuses connections, simulating a plugin that isn't
	// reachable this tick.
	pluginStore := &models.PluginStore{DB: conn}
	if err := pluginStore.Create(ctx, models.Plugin{ID: "bill-tracker", Name: "Bill Tracker", BaseURL: "http://127.0.0.1:0", Enabled: true, BootstrapManaged: true}); err != nil {
		t.Fatalf("Plugins.Create: %v", err)
	}

	cache := &plugins.VersionCache{}
	cache.Set("bill-tracker", &plugins.VersionInfo{Version: "1.0.0"})

	s := &Scheduler{Plugins: pluginStore, PluginVersions: cache}
	s.checkPluginVersions(ctx)

	got, ok := cache.Get("bill-tracker")
	if !ok || got.Version != "1.0.0" {
		t.Fatalf("expected the prior cached value to survive a fetch error, got %+v, %v", got, ok)
	}
}
