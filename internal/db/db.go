// Package db owns the Postgres connection pool used by the rest of the app.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// New opens a connection pool and verifies connectivity with a ping.
// We use database/sql (via the pgx stdlib adapter) rather than pgx's native
// pool API because it pairs nicely with goose for migrations and keeps the
// API surface small and familiar while you're learning Go.
func New(dsn string) (*sql.DB, error) {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening db: %w", err)
	}

	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("pinging db: %w", err)
	}

	return conn, nil
}
