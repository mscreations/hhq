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

package db

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate runs all pending goose migrations embedded in the binary.
// The app runs this automatically on startup (see cmd/server/main.go).
// This keeps deployment simple: no separate migration Job/init-container
// is required, though you could split it out later if you prefer that pattern.
func Migrate(conn *sql.DB) error {
	goose.SetBaseFS(migrationsFS)
	goose.SetTableName("hhq_goose_db_version")

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("setting goose dialect: %w", err)
	}

	if err := goose.Up(conn, "migrations"); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}

	return nil
}
