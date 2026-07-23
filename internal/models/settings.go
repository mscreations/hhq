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

package models

import (
	"context"
	"database/sql"
)

// SettingsStore backs the simple key/value settings table (app_title,
// timezone, etc. - see migration 00001_initial_schema.sql for seeded keys).
type SettingsStore struct {
	DB *sql.DB
}

// Get returns the value stored for key, or fallback if no row exists.
func (s *SettingsStore) Get(ctx context.Context, key, fallback string) (string, error) {
	var value string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM hhq_settings WHERE key = $1`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return fallback, nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

// Set upserts a key/value pair.
func (s *SettingsStore) Set(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO hhq_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
	return err
}

// CommonTimezones is a curated list of IANA zone names offered in the
// settings dropdown - not exhaustive (the full tzdata has ~600 entries),
// just a representative one per major region/UTC offset so the list stays
// short enough to pick from at a glance.
var CommonTimezones = []string{
	"UTC",
	"America/New_York",
	"America/Chicago",
	"America/Denver",
	"America/Phoenix",
	"America/Los_Angeles",
	"America/Anchorage",
	"Pacific/Honolulu",
	"America/Toronto",
	"America/Vancouver",
	"America/Mexico_City",
	"America/Sao_Paulo",
	"Europe/London",
	"Europe/Paris",
	"Europe/Berlin",
	"Europe/Madrid",
	"Europe/Rome",
	"Europe/Moscow",
	"Africa/Cairo",
	"Africa/Johannesburg",
	"Asia/Dubai",
	"Asia/Kolkata",
	"Asia/Shanghai",
	"Asia/Tokyo",
	"Asia/Seoul",
	"Asia/Singapore",
	"Asia/Hong_Kong",
	"Australia/Sydney",
	"Australia/Perth",
	"Pacific/Auckland",
}
