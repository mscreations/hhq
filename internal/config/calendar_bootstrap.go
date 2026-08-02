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
	"os"
	"strings"

	"github.com/mscreations/hhq/internal/models"
)

// CalendarAccountBootstrap is one entry in the CONFIG_DIR/calendars.json
// bootstrap file - see internal/handlers/sync.go's BootstrapCalendarAccounts
// for how these are reconciled against the database on every startup.
type CalendarAccountBootstrap struct {
	Name     string `json:"name"`
	Provider string `json:"provider"` // "fastmail", "icloud", "generic"/"caldav", or the raw calendar_provider enum values
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	// PasswordFile is an alternative to Password - a path to a file
	// containing just the password (e.g. a Kubernetes Secret volume mount),
	// so calendars.json itself can hold no secret material and live in a
	// plain ConfigMap. Mutually exclusive with Password - see
	// ResolvePassword.
	PasswordFile string `json:"password_file,omitempty"`
}

// ResolvePassword returns this entry's effective password: Password if set,
// or the trimmed contents of PasswordFile if that's set instead. Returns an
// error if both are set (ambiguous) or if PasswordFile can't be read. Called
// at bootstrap/reconcile time (not JSON-parse time) by
// BootstrapCalendarAccounts, so one bad entry's unreadable file doesn't fail
// the whole calendars.json.
func (e CalendarAccountBootstrap) ResolvePassword() (string, error) {
	if e.Password != "" && e.PasswordFile != "" {
		return "", fmt.Errorf("password and password_file are mutually exclusive")
	}
	if e.PasswordFile == "" {
		return e.Password, nil
	}
	data, err := os.ReadFile(e.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("reading password_file %q: %w", e.PasswordFile, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// ParseCalendarAccountsBootstrap unmarshals CONFIG_DIR/calendars.json's
// contents - a JSON array of CalendarAccountBootstrap entries. Per-entry
// validation (required fields, unknown provider) happens later in
// BootstrapCalendarAccounts, not here, so one malformed entry doesn't need to
// fail parsing of the whole file.
func ParseCalendarAccountsBootstrap(raw string) ([]CalendarAccountBootstrap, error) {
	var entries []CalendarAccountBootstrap
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parsing calendars.json: %w", err)
	}
	return entries, nil
}

// ResolveProvider maps a config file's provider string to the internal
// calendar_provider enum. Accepts both friendly aliases (fastmail, icloud,
// generic/caldav) and the raw enum spellings (caldav_fastmail, etc.),
// case-insensitively, so the config file doesn't need to know the internal
// enum naming.
func ResolveProvider(s string) (models.CalendarProvider, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fastmail", "caldav_fastmail":
		return models.ProviderFastmail, nil
	case "icloud", "caldav_icloud":
		return models.ProviderICloud, nil
	case "generic", "caldav", "caldav_generic":
		return models.ProviderGeneric, nil
	default:
		return "", fmt.Errorf("unknown calendar provider %q (expected fastmail, icloud, or generic)", s)
	}
}
