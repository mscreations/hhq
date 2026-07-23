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

package testutil

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// TestStartContainerErrorsForNonexistentImage covers startContainer's
// tcpostgres.Run-error branch: pointing postgresImage (see postgres.go) at
// an image that doesn't exist on any registry makes container startup fail,
// which must surface wrapped as "starting postgres container" rather than
// panicking. This calls startContainer directly (not through RequireDB), so
// it doesn't touch the shared once/sharedDB/sharedErr state the rest of the
// package relies on.
//
// Requires Docker itself to be reachable (to get far enough to attempt the
// pull and fail on "not found") - if Docker isn't available at all, this is
// skipped the same way RequireDB would skip, since there's nothing useful to
// assert about a pull failure when the daemon can't even be reached.
func TestStartContainerErrorsForNonexistentImage(t *testing.T) {
	if err := checkDockerAvailable(); err != nil {
		t.Skipf("skipping: docker not available: %v", err)
	}

	original := postgresImage
	postgresImage = "hhq-definitely-nonexistent-test-image-xyz:does-not-exist"
	defer func() { postgresImage = original }()

	_, err := startContainer()
	if err == nil {
		t.Fatal("expected startContainer to fail for a nonexistent image")
	}
	if got := err.Error(); len(got) == 0 {
		t.Fatal("expected a non-empty error message")
	}
	const wantPrefix = "starting postgres container"
	if !strings.Contains(err.Error(), wantPrefix) {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), wantPrefix)
	}
}

// TestRequireDBFailsFastWhenSharedContainerFailedToStart covers RequireDB's
// `if sharedErr != nil { t.Fatalf(...) }` branch: once the shared container
// has failed to start once (cached via sync.Once, see the package doc
// comment on `once`/`sharedDB`/`sharedErr`), every subsequent RequireDB call
// within the same test binary must fail fast with a clear message rather
// than hanging or silently returning a nil *sql.DB.
//
// Rather than actually breaking Docker (which would be both flaky and
// disruptive to every other DB-backed test in this process), this
// simulates an already-failed shared container by directly manipulating the
// package-level once/sharedErr/sharedDB vars from within the same package -
// exactly the state RequireDB itself reads. State is restored afterward so
// it can't leak into any other test in this package's binary.
//
// RequireDB's t.Fatalf must actually fail the *testing.T given to it, so
// this deliberately does NOT use t.Run (a failed subtest would mark this
// whole package's `go test` run as failed, which is not what we want to
// assert here - we want to assert RequireDB's *own* behavior, in isolation).
// Instead it hands RequireDB a scratch, not-registered-with-the-runner
// *testing.T and runs the call in its own goroutine, since Fatal's
// underlying FailNow calls runtime.Goexit(), which unwinds only the calling
// goroutine (running its deferred functions, including the one that signals
// completion here) rather than the whole process.
func TestRequireDBFailsFastWhenSharedContainerFailedToStart(t *testing.T) {
	if err := checkDockerAvailable(); err != nil {
		t.Skipf("skipping: docker not available: %v", err)
	}

	// sync.Once contains a sync.noCopy guard (enforced by `go vet`'s
	// copylocks check), so its previous value can't be saved off by copying
	// it into a local (`origOnce := once`) the way sharedDB/sharedErr are
	// below. In practice this doesn't need saving/restoring anyway: nothing
	// else in this package's own test binary calls RequireDB successfully
	// (the other tests in this file call startContainer directly, bypassing
	// the shared once/sharedDB/sharedErr singleton entirely), so replacing
	// `once` with a fresh zero-value sync.Once{} - both to force it into an
	// "already run" state for this test, and again afterward so no later
	// test in this binary is affected - is safe and doesn't discard any
	// state another test depends on.
	origDB := sharedDB
	origErr := sharedErr
	defer func() {
		once = sync.Once{}
		sharedDB = origDB
		sharedErr = origErr
	}()

	once = sync.Once{}
	once.Do(func() {}) // mark as already run, without actually starting a container
	sharedDB = nil
	sharedErr = errors.New("simulated: postgres container failed to start")

	scratch := &testing.T{}
	ranPastFatal := false
	done := make(chan struct{})
	go func() {
		defer close(done)
		RequireDB(scratch)
		ranPastFatal = true
	}()
	<-done

	if !scratch.Failed() {
		t.Fatal("expected RequireDB to mark its *testing.T as failed when sharedErr is set")
	}
	if ranPastFatal {
		t.Fatal("expected RequireDB's t.Fatalf to stop execution before returning")
	}
}

// TestTruncateAllNoOpsWhenNoTablesExist covers truncateAll's
// `if len(tables) == 0 { return nil }` branch. Every real test in this
// module runs truncateAll against the shared, already-migrated container (so
// hhq_* tables always exist by the time it's called via RequireDB's
// t.Cleanup), which never naturally exercises the zero-tables case. This
// spins up its own throwaway container (via startContainer, not the shared
// singleton, so it can't disturb any other test's shared state), drops
// everything in the public schema, and confirms truncateAll is a safe no-op
// against an empty schema rather than erroring on an empty table list.
func TestTruncateAllNoOpsWhenNoTablesExist(t *testing.T) {
	if err := checkDockerAvailable(); err != nil {
		t.Skipf("skipping: docker not available: %v", err)
	}

	conn, err := startContainer()
	if err != nil {
		t.Fatalf("startContainer: %v", err)
	}
	// Not the shared container, so no cleanup registration needed for other
	// tests' sake - it's simply abandoned like every other throwaway
	// container startContainer creates (see its own comment: testcontainers'
	// Ryuk reaper cleans it up after the process exits).

	if _, err := conn.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("dropping all tables: %v", err)
	}

	if err := truncateAll(conn); err != nil {
		t.Fatalf("truncateAll against an empty schema: %v", err)
	}
}
