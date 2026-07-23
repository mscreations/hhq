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

package handlers

import (
	"html/template"
	"net/http/httptest"
	"testing"
)

// TestRenderFragmentTemplateExecutionError covers renderFragment's
// template.Execute-error branch. Every real template in this app is fixed
// and embedded, so it can't fail in practice - this test adds a throwaway
// broken template (referencing a field the passed data doesn't have) to the
// same *template.Template tree purely to exercise the error-handling branch
// itself, without touching any real template file.
func TestRenderFragmentTemplateExecutionError(t *testing.T) {
	tmpl := template.Must(template.New("root").Parse(""))
	template.Must(tmpl.New("test/broken").Parse("{{.NoSuchField}}"))
	app := &App{Templates: tmpl}

	w := httptest.NewRecorder()
	app.renderFragment(w, "test/broken", struct{ Unrelated string }{Unrelated: "x"})

	if w.Code < 500 {
		t.Fatalf("status = %d, want a 5xx from the template execution error", w.Code)
	}
}
