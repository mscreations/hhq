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
