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

import "testing"

func TestParsePluginsBootstrapValidJSON(t *testing.T) {
	raw := `[
		{"id": "bill-tracker", "name": "Bill Tracker", "base_url": "http://billtracker:8090", "enabled": true},
		{"id": "chore-extras", "name": "Chore Extras", "base_url": "http://chore-extras:9000", "enabled": false}
	]`

	entries, err := ParsePluginsBootstrap(raw)
	if err != nil {
		t.Fatalf("ParsePluginsBootstrap: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].ID != "bill-tracker" || entries[0].Name != "Bill Tracker" ||
		entries[0].BaseURL != "http://billtracker:8090" || !entries[0].Enabled {
		t.Fatalf("entries[0] = %+v", entries[0])
	}
	if entries[1].ID != "chore-extras" || entries[1].Enabled {
		t.Fatalf("entries[1] = %+v", entries[1])
	}
}

func TestParsePluginsBootstrapEmptyArray(t *testing.T) {
	entries, err := ParsePluginsBootstrap(`[]`)
	if err != nil {
		t.Fatalf("ParsePluginsBootstrap: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0", len(entries))
	}
}

// TestParsePluginsBootstrapMissingFieldsDeferredToBootstrapPlugins confirms
// ParsePluginsBootstrap itself does no per-entry validation - an entry
// missing id/base_url still parses successfully, since that check happens
// later in BootstrapPlugins (internal/handlers/plugin_bootstrap.go) so one
// malformed entry doesn't fail parsing of the whole file.
func TestParsePluginsBootstrapMissingFieldsDeferredToBootstrapPlugins(t *testing.T) {
	entries, err := ParsePluginsBootstrap(`[{"name": "No ID or URL"}]`)
	if err != nil {
		t.Fatalf("ParsePluginsBootstrap: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].ID != "" || entries[0].BaseURL != "" {
		t.Fatalf("entries[0] = %+v, want empty ID/BaseURL to pass through unvalidated", entries[0])
	}
}

func TestParsePluginsBootstrapInvalidJSON(t *testing.T) {
	if _, err := ParsePluginsBootstrap(`not json`); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}
