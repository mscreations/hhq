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
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/release"
	"github.com/mscreations/hhq/internal/testutil"
)

// TestCheckPluginUpdatesCachesNewerVersion is the "update available" case:
// a plugin's stored version is older than what its repo_url's fake GitHub
// server reports, so checkPluginUpdates must cache the newer release.
func TestCheckPluginUpdatesCachesNewerVersion(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"tag_name": "v2.1.0",
			"html_url": "https://example.com/v2.1.0",
		})
	}))
	defer srv.Close()

	pluginStore := &models.PluginStore{DB: conn}
	if err := pluginStore.Create(ctx, models.Plugin{
		ID: "bill-tracker", Name: "Bill Tracker", BaseURL: "http://plugin.local", Enabled: true,
		RepoURL: sql.NullString{String: srv.URL, Valid: true},
	}); err != nil {
		t.Fatalf("Plugins.Create: %v", err)
	}
	if err := pluginStore.UpdateManifest(ctx, "bill-tracker", false, sql.NullString{String: "2.0.0", Valid: true}); err != nil {
		t.Fatalf("UpdateManifest: %v", err)
	}

	s := &Scheduler{Plugins: pluginStore, PluginReleases: &release.PluginCache{}}
	s.checkPluginUpdates(ctx)

	got, ok := s.PluginReleases.Get("bill-tracker")
	if !ok {
		t.Fatal("expected a cached release for bill-tracker")
	}
	if got.Version != "2.1.0" {
		t.Errorf("Version = %q, want %q", got.Version, "2.1.0")
	}
}

// TestCheckPluginUpdatesSkipsPluginsWithoutRepoURLOrVersion confirms plugins
// that haven't opted into update checking (no repo_url) or haven't reported
// a version yet (no manifest fetch has succeeded) are silently skipped
// rather than erroring or making any HTTP request.
func TestCheckPluginUpdatesSkipsPluginsWithoutRepoURLOrVersion(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	requestMade := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMade = true
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v9.9.9"})
	}))
	defer srv.Close()

	pluginStore := &models.PluginStore{DB: conn}
	// No repo_url configured.
	if err := pluginStore.Create(ctx, models.Plugin{ID: "no-repo", Name: "No Repo", BaseURL: "http://plugin.local", Enabled: true}); err != nil {
		t.Fatalf("Plugins.Create: %v", err)
	}
	if err := pluginStore.UpdateManifest(ctx, "no-repo", false, sql.NullString{String: "1.0.0", Valid: true}); err != nil {
		t.Fatalf("UpdateManifest: %v", err)
	}
	// repo_url configured but no version reported yet.
	if err := pluginStore.Create(ctx, models.Plugin{
		ID: "no-version", Name: "No Version", BaseURL: "http://plugin.local", Enabled: true,
		RepoURL: sql.NullString{String: srv.URL, Valid: true},
	}); err != nil {
		t.Fatalf("Plugins.Create: %v", err)
	}

	s := &Scheduler{Plugins: pluginStore, PluginReleases: &release.PluginCache{}}
	s.checkPluginUpdates(ctx)

	if requestMade {
		t.Error("expected no HTTP request for plugins without both repo_url and a known version")
	}
	if _, ok := s.PluginReleases.Get("no-repo"); ok {
		t.Error("did not expect a cached release for a plugin with no repo_url")
	}
	if _, ok := s.PluginReleases.Get("no-version"); ok {
		t.Error("did not expect a cached release for a plugin with no known version")
	}
}
