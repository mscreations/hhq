package config

import (
	"errors"
	"os"
	"path/filepath"
)

// DefaultConfigDir is used when CONFIG_DIR is unset.
const DefaultConfigDir = "/config"

// ConfigDir returns the directory main.go scans on startup for the bootstrap
// config files (calendars.json, children.json, chores.json,
// assignments.json - see internal/handlers/bootstrap_config.go and
// BootstrapCalendarAccounts), defaulting to DefaultConfigDir if CONFIG_DIR
// isn't set.
func ConfigDir() string {
	return getEnvDefault("CONFIG_DIR", DefaultConfigDir)
}

// ReadBootstrapFile reads filename from configDir and returns its contents.
// Each bootstrap file is independently optional: if the file doesn't exist,
// this returns ("", nil) rather than an error, so a deployment can supply
// any subset of calendars.json/children.json/chores.json/assignments.json.
func ReadBootstrapFile(configDir, filename string) (string, error) {
	data, err := os.ReadFile(filepath.Join(configDir, filename))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}
