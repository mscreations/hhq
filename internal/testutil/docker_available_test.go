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
