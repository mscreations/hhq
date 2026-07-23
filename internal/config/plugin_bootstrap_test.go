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
