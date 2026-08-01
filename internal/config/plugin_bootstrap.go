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

package config

import (
	"encoding/json"
	"fmt"
)

// PluginBootstrap is one entry in the CONFIG_DIR/plugins.json bootstrap
// file - see internal/handlers/plugin_bootstrap.go's BootstrapPlugins for
// how these are reconciled against the database on every startup.
type PluginBootstrap struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Enabled bool   `json:"enabled"`
	// RepoURL is optional - e.g. "https://github.com/mscreations/billtracker-plugin".
	// When set, hhq periodically checks that repo's GitHub Releases/Tags for
	// a newer version than the plugin's currently-reported one and shows an
	// update-available icon on the parent dashboard (see internal/release
	// and internal/scheduler's checkPluginUpdates). Left unset, the plugin
	// simply never gets an update check.
	RepoURL string `json:"repo_url,omitempty"`
}

// ParsePluginsBootstrap unmarshals CONFIG_DIR/plugins.json's contents - a
// JSON array of PluginBootstrap entries. Per-entry validation (missing
// id/base_url) happens later in BootstrapPlugins, not here, so one
// malformed entry doesn't fail parsing of the whole file.
func ParsePluginsBootstrap(raw string) ([]PluginBootstrap, error) {
	var entries []PluginBootstrap
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parsing plugins.json: %w", err)
	}
	return entries, nil
}
