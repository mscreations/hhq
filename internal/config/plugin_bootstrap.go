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
