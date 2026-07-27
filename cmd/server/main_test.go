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

package main

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
	"github.com/mscreations/hhq/internal/util"
)

func TestDebugRequestLoggerCallsNextHandler(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/some/path", nil)
	req.AddCookie(&http.Cookie{Name: cookieNameForLogging, Value: "some-session-token"})
	rec := httptest.NewRecorder()

	debugRequestLogger(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected debugRequestLogger to call the wrapped handler")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestTemplateNamesListsOnlyNamedTemplates(t *testing.T) {
	tmpl := template.Must(template.New("root").Parse(`{{define "a"}}A{{end}}{{define "b"}}B{{end}}`))

	names := templateNames(tmpl)
	sort.Strings(names)

	want := []string{"a", "b", "root"}
	sort.Strings(want)
	if len(names) != len(want) {
		t.Fatalf("templateNames = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("templateNames = %v, want %v", names, want)
		}
	}
}

func TestBootstrapFirstParentSkipsWhenAParentAlreadyExists(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &models.UserStore{DB: conn}
	ctx := t.Context()

	if _, err := users.CreateParent(ctx, "Existing", "existing@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	t.Setenv("BOOTSTRAP_PARENT_EMAIL", "bootstrap@example.com")
	t.Setenv("BOOTSTRAP_PARENT_PASSWORD", "some-password")

	if err := bootstrapFirstParent(ctx, users); err != nil {
		t.Fatalf("bootstrapFirstParent: %v", err)
	}

	parents, err := users.ListParents(ctx)
	if err != nil {
		t.Fatalf("ListParents: %v", err)
	}
	if len(parents) != 1 {
		t.Fatalf("expected bootstrap to be a no-op when a parent already exists, got %d parents", len(parents))
	}
}

func TestBootstrapFirstParentNoOpsWithoutEnvVars(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &models.UserStore{DB: conn}
	ctx := t.Context()

	t.Setenv("BOOTSTRAP_PARENT_EMAIL", "")
	t.Setenv("BOOTSTRAP_PARENT_PASSWORD", "")

	if err := bootstrapFirstParent(ctx, users); err != nil {
		t.Fatalf("bootstrapFirstParent: %v", err)
	}

	parents, err := users.ListParents(ctx)
	if err != nil {
		t.Fatalf("ListParents: %v", err)
	}
	if len(parents) != 0 {
		t.Fatalf("expected no parent created without BOOTSTRAP_PARENT_* env vars, got %d", len(parents))
	}
}

func TestBootstrapFirstParentCreatesParentFromEnvVars(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &models.UserStore{DB: conn}
	ctx := t.Context()

	t.Setenv("BOOTSTRAP_PARENT_EMAIL", "bootstrap@example.com")
	t.Setenv("BOOTSTRAP_PARENT_PASSWORD", "some-password")
	t.Setenv("BOOTSTRAP_PARENT_NAME", "Bootstrap Parent")

	if err := bootstrapFirstParent(ctx, users); err != nil {
		t.Fatalf("bootstrapFirstParent: %v", err)
	}

	parents, err := users.ListParents(ctx)
	if err != nil {
		t.Fatalf("ListParents: %v", err)
	}
	if len(parents) != 1 || parents[0].Name != "Bootstrap Parent" || parents[0].Email.String != "bootstrap@example.com" {
		t.Fatalf("parents = %+v, want a single bootstrapped parent", parents)
	}
	if !parents[0].PasswordHash.Valid {
		t.Fatal("expected the bootstrapped parent to have a password hash set")
	}
}

func TestBootstrapFirstParentCreatesParentFromEnvFiles(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &models.UserStore{DB: conn}
	ctx := t.Context()

	dir := t.TempDir()
	emailPath := filepath.Join(dir, "email.txt")
	passwordPath := filepath.Join(dir, "password.txt")
	namePath := filepath.Join(dir, "name.txt")

	if err := os.WriteFile(emailPath, []byte("bootstrap@example.com"), 0600); err != nil {
		t.Fatalf("failed to write email file: %v", err)
	}
	if err := os.WriteFile(passwordPath, []byte("some-password"), 0600); err != nil {
		t.Fatalf("failed to write password file: %v", err)
	}
	if err := os.WriteFile(namePath, []byte("Bootstrap Parent"), 0600); err != nil {
		t.Fatalf("failed to write name file: %v", err)
	}
	t.Setenv("BOOTSTRAP_PARENT_EMAIL_FILE", emailPath)
	t.Setenv("BOOTSTRAP_PARENT_PASSWORD_FILE", passwordPath)
	t.Setenv("BOOTSTRAP_PARENT_NAME_FILE", namePath)

	if err := bootstrapFirstParent(ctx, users); err != nil {
		t.Fatalf("bootstrapFirstParent: %v", err)
	}

	parents, err := users.ListParents(ctx)
	if err != nil {
		t.Fatalf("ListParents: %v", err)
	}
	if len(parents) != 1 || parents[0].Name != "Bootstrap Parent" || parents[0].Email.String != "bootstrap@example.com" {
		t.Fatalf("parents = %+v, want a single bootstrapped parent", parents)
	}
	if !parents[0].PasswordHash.Valid {
		t.Fatal("expected the bootstrapped parent to have a password hash set")
	}
}

func testEncryptor(t *testing.T) *util.Encryptor {
	t.Helper()
	enc, err := util.NewEncryptor("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	return enc
}

func TestLoadOrGenerateSecretReturnsEnvValueWithoutTouchingSettings(t *testing.T) {
	conn := testutil.RequireDB(t)
	settings := &models.SettingsStore{DB: conn}
	ctx := t.Context()

	got, err := loadOrGenerateSecret(ctx, settings, testEncryptor(t), "explicit-secret", "some_key", "SOME_SECRET")
	if err != nil {
		t.Fatalf("loadOrGenerateSecret: %v", err)
	}
	if got != "explicit-secret" {
		t.Fatalf("got %q, want the explicit env value", got)
	}

	stored, err := settings.Get(ctx, "some_key", "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored != "" {
		t.Fatalf("expected no settings row to be written when an env value is provided, got %q", stored)
	}
}

func TestLoadOrGenerateSecretGeneratesAndPersistsWhenUnset(t *testing.T) {
	conn := testutil.RequireDB(t)
	settings := &models.SettingsStore{DB: conn}
	enc := testEncryptor(t)
	ctx := t.Context()

	got, err := loadOrGenerateSecret(ctx, settings, enc, "", "some_key", "SOME_SECRET")
	if err != nil {
		t.Fatalf("loadOrGenerateSecret: %v", err)
	}
	if len(got) != 64 {
		t.Fatalf("generated secret length = %d, want 64 hex chars", len(got))
	}

	stored, err := settings.Get(ctx, "some_key", "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored == "" {
		t.Fatal("expected the generated secret to be persisted encrypted in settings")
	}
	if stored == got {
		t.Fatal("expected the stored settings value to be encrypted, not the plaintext secret")
	}

	// A second call should decrypt and return the same secret rather than
	// generating a new one, so the value is stable across restarts.
	got2, err := loadOrGenerateSecret(ctx, settings, enc, "", "some_key", "SOME_SECRET")
	if err != nil {
		t.Fatalf("loadOrGenerateSecret (second call): %v", err)
	}
	if got2 != got {
		t.Fatalf("second call returned %q, want the same persisted secret %q", got2, got)
	}
}

func TestBootstrapFirstParentDefaultsNameWhenUnset(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &models.UserStore{DB: conn}
	ctx := t.Context()

	t.Setenv("BOOTSTRAP_PARENT_EMAIL", "bootstrap-noname@example.com")
	t.Setenv("BOOTSTRAP_PARENT_PASSWORD", "some-password")
	t.Setenv("BOOTSTRAP_PARENT_NAME", "")

	if err := bootstrapFirstParent(ctx, users); err != nil {
		t.Fatalf("bootstrapFirstParent: %v", err)
	}

	parents, err := users.ListParents(ctx)
	if err != nil {
		t.Fatalf("ListParents: %v", err)
	}
	if len(parents) != 1 || parents[0].Name != "Parent" {
		t.Fatalf("parents = %+v, want the default name %q", parents, "Parent")
	}
}

// TestBootstrapFirstParentAppliesAvatarFile covers BOOTSTRAP_PARENT_AVATAR_FILE
// being applied to the freshly-created initial parent, via the same
// handlers.ApplyUserAvatarFile helper BootstrapChildren's avatar_file uses.
func TestBootstrapFirstParentAppliesAvatarFile(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &models.UserStore{DB: conn}
	ctx := t.Context()

	path := filepath.Join(t.TempDir(), "parent-avatar.png")
	if err := os.WriteFile(path, tinyPNGAvatar, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	t.Setenv("BOOTSTRAP_PARENT_EMAIL", "bootstrap-avatar@example.com")
	t.Setenv("BOOTSTRAP_PARENT_PASSWORD", "some-password")
	t.Setenv("BOOTSTRAP_PARENT_AVATAR_FILE", path)

	if err := bootstrapFirstParent(ctx, users); err != nil {
		t.Fatalf("bootstrapFirstParent: %v", err)
	}

	parent, err := users.GetByEmail(ctx, "bootstrap-avatar@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if !parent.HasAvatar {
		t.Fatal("expected BOOTSTRAP_PARENT_AVATAR_FILE to be applied to the newly-created parent")
	}
}

// TestBootstrapFirstParentSkipsAvatarFileWhenAlreadyAParent covers
// BOOTSTRAP_PARENT_AVATAR_FILE being ignored (like the rest of
// bootstrapFirstParent) once a parent already exists - the env var should
// never retroactively change an existing parent's avatar.
func TestBootstrapFirstParentSkipsAvatarFileWhenAlreadyAParent(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &models.UserStore{DB: conn}
	ctx := t.Context()

	if _, err := users.CreateParent(ctx, "Existing", "existing-avatar@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	path := filepath.Join(t.TempDir(), "parent-avatar.png")
	if err := os.WriteFile(path, tinyPNGAvatar, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	t.Setenv("BOOTSTRAP_PARENT_EMAIL", "bootstrap-avatar2@example.com")
	t.Setenv("BOOTSTRAP_PARENT_PASSWORD", "some-password")
	t.Setenv("BOOTSTRAP_PARENT_AVATAR_FILE", path)

	if err := bootstrapFirstParent(ctx, users); err != nil {
		t.Fatalf("bootstrapFirstParent: %v", err)
	}

	existing, err := users.GetByEmail(ctx, "existing-avatar@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if existing.HasAvatar {
		t.Fatal("expected the pre-existing parent's avatar to be untouched")
	}
}
