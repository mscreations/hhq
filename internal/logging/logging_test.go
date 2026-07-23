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

package logging

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// withLevel temporarily overrides the package-level current level (set once
// by init() from LOG_LEVEL at program startup) and restores it afterward -
// this lets tests exercise each level's gating logic without depending on
// the process's actual LOG_LEVEL env var.
func withLevel(t *testing.T, level Level) {
	t.Helper()
	prev := current
	current = level
	t.Cleanup(func() { current = prev })
}

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	fn()
	return buf.String()
}

func TestDebugfOnlyLogsAtDebugLevel(t *testing.T) {
	withLevel(t, LevelDebug)
	out := captureLog(t, func() { Debugf("hello %s", "world") })
	if !strings.Contains(out, "[DEBUG] hello world") {
		t.Fatalf("output = %q, want it to contain the debug message", out)
	}

	withLevel(t, LevelInfo)
	out = captureLog(t, func() { Debugf("should be suppressed") })
	if out != "" {
		t.Fatalf("output = %q, want no output at LevelInfo", out)
	}
}

func TestInfofRespectsLevel(t *testing.T) {
	withLevel(t, LevelWarn)
	out := captureLog(t, func() { Infof("suppressed") })
	if out != "" {
		t.Fatalf("output = %q, want no output at LevelWarn", out)
	}

	withLevel(t, LevelInfo)
	out = captureLog(t, func() { Infof("shown") })
	if !strings.Contains(out, "[INFO] shown") {
		t.Fatalf("output = %q, want it to contain the info message", out)
	}
}

func TestWarnfRespectsLevel(t *testing.T) {
	withLevel(t, LevelError)
	out := captureLog(t, func() { Warnf("suppressed") })
	if out != "" {
		t.Fatalf("output = %q, want no output at LevelError", out)
	}

	withLevel(t, LevelWarn)
	out = captureLog(t, func() { Warnf("shown") })
	if !strings.Contains(out, "[WARN] shown") {
		t.Fatalf("output = %q, want it to contain the warn message", out)
	}
}

func TestErrorfAlwaysLogsRegardlessOfLevel(t *testing.T) {
	withLevel(t, LevelError)
	out := captureLog(t, func() { Errorf("always shown") })
	if !strings.Contains(out, "[ERROR] always shown") {
		t.Fatalf("output = %q, want the error message even at LevelError", out)
	}
}

func TestLevelFromEnvParsesAllRecognizedValues(t *testing.T) {
	cases := []struct {
		input string
		want  Level
	}{
		{"debug", LevelDebug},
		{"DEBUG", LevelDebug},
		{" debug ", LevelDebug},
		{"warn", LevelWarn},
		{"warning", LevelWarn},
		{"error", LevelError},
		{"", LevelInfo},
		{"nonsense", LevelInfo},
	}
	for _, c := range cases {
		if got := levelFromEnv(c.input); got != c.want {
			t.Errorf("levelFromEnv(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

func TestDebugEnabledReflectsCurrentLevel(t *testing.T) {
	withLevel(t, LevelDebug)
	if !DebugEnabled() {
		t.Error("expected DebugEnabled() to be true at LevelDebug")
	}

	withLevel(t, LevelInfo)
	if DebugEnabled() {
		t.Error("expected DebugEnabled() to be false at LevelInfo")
	}
}
