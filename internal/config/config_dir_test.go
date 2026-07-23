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
	"os"
	"path/filepath"
	"testing"
)

func TestConfigDirDefault(t *testing.T) {
	os.Unsetenv("CONFIG_DIR")
	if got := ConfigDir(); got != DefaultConfigDir {
		t.Fatalf("ConfigDir() = %q, want %q", got, DefaultConfigDir)
	}
}

func TestConfigDirOverride(t *testing.T) {
	t.Setenv("CONFIG_DIR", "/custom/config")
	if got := ConfigDir(); got != "/custom/config" {
		t.Fatalf("ConfigDir() = %q, want /custom/config", got)
	}
}

func TestReadBootstrapFileMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	got, err := ReadBootstrapFile(dir, "children.json")
	if err != nil {
		t.Fatalf("ReadBootstrapFile: %v", err)
	}
	if got != "" {
		t.Fatalf("ReadBootstrapFile for missing file = %q, want empty", got)
	}
}

// TestReadBootstrapFileReturnsOtherReadErrors covers ReadBootstrapFile's
// branch for a read failure that isn't os.ErrNotExist - such errors must be
// propagated to the caller, not swallowed the way a missing file is. A
// directory where a file is expected reproduces this without relying on
// platform-specific permission behavior.
func TestReadBootstrapFileReturnsOtherReadErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "children.json"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	got, err := ReadBootstrapFile(dir, "children.json")
	if err == nil {
		t.Fatal("expected ReadBootstrapFile to return an error when the path is a directory")
	}
	if os.IsNotExist(err) {
		t.Fatalf("expected a non-not-exist error, got %v", err)
	}
	if got != "" {
		t.Fatalf("ReadBootstrapFile on error = %q, want empty", got)
	}
}

func TestReadBootstrapFileReturnsContents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "children.json"), []byte(`[{"name":"Kid"}]`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := ReadBootstrapFile(dir, "children.json")
	if err != nil {
		t.Fatalf("ReadBootstrapFile: %v", err)
	}
	if got != `[{"name":"Kid"}]` {
		t.Fatalf("ReadBootstrapFile = %q", got)
	}
}
