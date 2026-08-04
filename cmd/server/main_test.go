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
