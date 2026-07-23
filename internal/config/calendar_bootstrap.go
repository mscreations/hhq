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
