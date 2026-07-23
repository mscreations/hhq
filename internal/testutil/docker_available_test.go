package testutil

import "testing"

// TestDockerAvailableForIntegrationTests exists purely as a loud, default-
// visible signal: go test only prints a skipped test's message with -v, so
// RequireDB's per-call t.Skipf (in every DB-backed test, across every
// package) is easy to miss and was mistaken once already for a real
// coverage regression rather than an environment problem (missing Docker).
// This test fails - rather than skips - so its message shows up in plain
// `go test ./...` output, pointing straight at the cause.
func TestDockerAvailableForIntegrationTests(t *testing.T) {
	if err := checkDockerAvailable(); err != nil {
		t.Errorf("Docker is not available, so every DB-backed test across the "+
			"module (internal/models, internal/handlers, internal/scheduler, "+
			"internal/caldav, cmd/server, ...) just skipped rather than ran: %v. "+
			"Start Docker (or Docker Desktop) and re-run `go test ./...` for real coverage.", err)
	}
}
