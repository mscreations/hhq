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
