package db_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/mscreations/hhq/internal/db"
	"github.com/mscreations/hhq/internal/testutil"
)

// TestNewErrorsForMalformedDSN covers New's ping-error branch. The pgx
// stdlib driver parses the DSN lazily, at connect time rather than at
// sql.Open time (matching database/sql's own documented "Open may just
// validate its arguments without creating a connection" behavior), so a
// malformed DSN surfaces here as a ping failure, not an Open failure.
func TestNewErrorsForMalformedDSN(t *testing.T) {
	_, err := db.New("this is not a valid postgres dsn")
	if err == nil {
		t.Fatal("expected New to fail for a malformed DSN")
	}
	if !strings.Contains(err.Error(), "pinging db") {
		t.Fatalf("error = %v, want it to include the 'pinging db' wrap context", err)
	}
}

// TestNewErrorsForUnreachableHost covers the same ping-error branch via a
// syntactically valid DSN pointing at nothing listening. connect_timeout=1
// keeps this fast rather than relying on New's full 5s ping timeout.
func TestNewErrorsForUnreachableHost(t *testing.T) {
	_, err := db.New("postgres://user:pass@127.0.0.1:1/nonexistent?sslmode=disable&connect_timeout=1")
	if err == nil {
		t.Fatal("expected New to fail for an unreachable host")
	}
	if !strings.Contains(err.Error(), "pinging db") {
		t.Fatalf("error = %v, want it to include the 'pinging db' wrap context", err)
	}
}

// TestMigrateErrorsWhenConnectionUnusable covers Migrate's goose.Up-error
// branch: unlike New (which pings first), Migrate is handed a *sql.DB
// directly with no connectivity check of its own, so an unusable connection
// must still surface as a wrapped error rather than panicking or hanging.
func TestMigrateErrorsWhenConnectionUnusable(t *testing.T) {
	conn, err := sql.Open("pgx", "postgres://user:pass@127.0.0.1:1/nonexistent?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer conn.Close()

	err = db.Migrate(conn)
	if err == nil {
		t.Fatal("expected Migrate to fail against an unreachable connection")
	}
	if !strings.Contains(err.Error(), "running migrations") {
		t.Fatalf("error = %v, want it to include the 'running migrations' wrap context", err)
	}
}

// TestMigrateIsIdempotent confirms Migrate can be safely re-run against an
// already-migrated database - main.go calls it unconditionally on every
// startup, relying on goose's own idempotency. The second call must
// succeed with nothing pending to apply.
func TestMigrateIsIdempotent(t *testing.T) {
	conn := testutil.RequireDB(t)
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate (second run): %v", err)
	}
}
