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

// Package testutil provides shared test helpers, primarily a real,
// migrated Postgres instance backed by testcontainers-go. Model and
// handler integration tests use this rather than mocking database/sql,
// since this app relies on Postgres-specific SQL (make_interval,
// ON CONFLICT, array containment, CHECK constraints) that a generic mock
// can't faithfully exercise. See CLAUDE.md round 3 for the class of bug
// (SQL type inference) this is meant to catch.
package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/mscreations/hhq/internal/db"
)

// One Postgres container is started per test binary (i.e. per package under
// test) and reused across every test in that package, rather than one
// container per test — spinning up Postgres takes ~1.5s, which would
// dominate runtime for packages with dozens of test cases. Isolation
// between tests is achieved by truncating all app tables after each test
// (see RequireDB) instead of tearing down and recreating the container.
var (
	once      sync.Once
	sharedDB  *sql.DB
	sharedErr error
)

// RequireDB skips the test unless Docker is available (testcontainers-go
// needs it to launch Postgres). It returns a migrated *sql.DB shared with
// other tests in the same package, and registers a cleanup that truncates
// all app tables after this test so the next test starts from empty tables.
//
// See TestDockerAvailableForIntegrationTests (docker_available_test.go) for
// the loud, default-visible signal when Docker isn't reachable — go test
// only prints a skipped test's message with -v, so that per-call t.Skipf
// here is easy to miss and was mistaken for a real coverage regression once
// already (see CLAUDE.md's testutil notes).
func RequireDB(t *testing.T) *sql.DB {
	t.Helper()

	if err := checkDockerAvailable(); err != nil {
		t.Skipf("skipping: docker not available for testcontainers: %v", err)
	}

	once.Do(func() {
		sharedDB, sharedErr = startContainer()
	})
	if sharedErr != nil {
		t.Fatalf("starting shared test postgres: %v", sharedErr)
	}

	t.Cleanup(func() {
		if err := truncateAll(sharedDB); err != nil {
			t.Logf("truncating tables after test: %v", err)
		}
	})

	return sharedDB
}

// postgresImage is the image startContainer launches. It's a var (not a
// const) purely so a test can point it at a deliberately-nonexistent image
// to exercise the tcpostgres.Run error branch below - every real call path
// leaves it at this default.
var postgresImage = "postgres:16-alpine"

func startContainer() (*sql.DB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx,
		postgresImage,
		tcpostgres.WithDatabase("hhq_test"),
		tcpostgres.WithUsername("hhq"),
		tcpostgres.WithPassword("hhq"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("starting postgres container: %w", err)
	}
	// Deliberately not terminated here: it lives for the lifetime of the
	// test binary process. testcontainers' Ryuk reaper cleans it up after
	// this process exits, which is good enough for test runs.

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("getting connection string: %w", err)
	}

	conn, err := db.New(dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting to test postgres: %w", err)
	}

	if err := db.Migrate(conn); err != nil {
		return nil, fmt.Errorf("running migrations against test postgres: %w", err)
	}

	return conn, nil
}

// truncateAll clears every app table (everything except goose's own
// bookkeeping table) and resets identity sequences, so each test starts
// with empty tables and predictable auto-incremented IDs.
func truncateAll(conn *sql.DB) error {
	rows, err := conn.Query(`
		SELECT tablename FROM pg_tables
		WHERE schemaname = 'public' AND tablename NOT IN ('hhq_goose_db_version')`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	if len(tables) == 0 {
		return nil
	}

	query := "TRUNCATE TABLE"
	for i, tbl := range tables {
		if i > 0 {
			query += ","
		}
		query += " " + tbl
	}
	query += " RESTART IDENTITY CASCADE"

	_, err = conn.Exec(query)
	return err
}

func checkDockerAvailable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return err
	}
	defer provider.Close()
	return provider.Health(ctx)
}
