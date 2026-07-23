package plugins

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestInjectBackLinkWithBodyTag(t *testing.T) {
	html := `<html><head></head><body class="x"><p>hi</p></body></html>`
	out := injectBackLink(html)
	if !strings.Contains(out, backLinkHTML) {
		t.Fatalf("expected back link injected, got %q", out)
	}
	bodyIdx := strings.Index(out, `<body class="x">`)
	linkIdx := strings.Index(out, backLinkHTML)
	if bodyIdx == -1 || linkIdx == -1 || linkIdx < bodyIdx {
		t.Fatalf("expected back link right after <body>, got %q", out)
	}
}

func TestInjectBackLinkWithoutBodyTag(t *testing.T) {
	html := `<p>just a fragment</p>`
	out := injectBackLink(html)
	if !strings.HasPrefix(out, backLinkHTML) {
		t.Fatalf("expected back link prepended, got %q", out)
	}
}

func TestInjectCSRFTokens(t *testing.T) {
	html := `<form method="POST" action="/x"><input name="a"></form><form method="GET"></form>`
	out := injectCSRFTokens(html, "tok123")
	if strings.Count(out, `name="csrf_token" value="tok123"`) != 1 {
		t.Fatalf("expected exactly one csrf token injected into the POST form, got %q", out)
	}
}

func TestProxySettingsGetHTML(t *testing.T) {
	plugin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.Path != "/settings" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><body><form method="POST" action=""><button>Go</button></form></body></html>`))
	}))
	t.Cleanup(plugin.Close)

	req := httptest.NewRequest(http.MethodGet, "/parent/plugins/x/settings", nil)
	rec := httptest.NewRecorder()

	ProxySettings(rec, req, plugin.URL, "tok", "csrf-abc")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="csrf_token" value="csrf-abc"`) {
		t.Fatalf("expected csrf token injected, got %s", body)
	}
	if !strings.Contains(body, "Back to Dashboard") {
		t.Fatalf("expected back link injected, got %s", body)
	}
}

func TestProxySettingsPostFormRebuiltFromPostForm(t *testing.T) {
	var gotBody string
	plugin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<p>ok</p>`))
	}))
	t.Cleanup(plugin.Close)

	req := httptest.NewRequest(http.MethodPost, "/parent/plugins/x/settings", strings.NewReader("ignored=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Simulate VerifyCSRF middleware already having called r.ParseForm,
	// which drains r.Body and populates r.PostForm - ProxySettings must
	// rebuild the outgoing body from PostForm, not the (now-empty) r.Body.
	req.PostForm = url.Values{"action": {"noop"}, "csrf_token": {"tok"}}
	rec := httptest.NewRecorder()

	ProxySettings(rec, req, plugin.URL, "tok", "csrf-xyz")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotBody, "action=noop") {
		t.Fatalf("expected the plugin to receive the PostForm-encoded body, got %q", gotBody)
	}
}

func TestProxySettingsRequestCreationError(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/parent/plugins/x/settings", nil)
	rec := httptest.NewRecorder()

	err := ProxySettings(rec, req, ":not-a-url", "tok", "csrf")

	if err != nil {
		t.Fatalf("err = %v, want nil (response already written)", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestProxySettingsDoError(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/parent/plugins/x/settings", nil)
	rec := httptest.NewRecorder()

	err := ProxySettings(rec, req, "http://127.0.0.1:1", "tok", "csrf")

	if err == nil {
		t.Fatal("expected a non-nil error for an unreachable plugin, got nil")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v", err)
	}
	if rec.Code != 200 {
		t.Fatalf("status = %d, want no response written by ProxySettings itself (caller decides)", rec.Code)
	}
}

func TestProxySettingsHTMLReadBodyError(t *testing.T) {
	plugin := brokenBodyServer(t)

	req := httptest.NewRequest(http.MethodGet, "/parent/plugins/x/settings", nil)
	rec := httptest.NewRecorder()

	ProxySettings(rec, req, plugin.URL, "tok", "csrf")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body = %s", rec.Code, rec.Body.String())
	}
}

func TestProxySettingsNonHTMLCopiesBodyDirectly(t *testing.T) {
	plugin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(plugin.Close)

	req := httptest.NewRequest(http.MethodGet, "/parent/plugins/x/settings", nil)
	rec := httptest.NewRecorder()

	ProxySettings(rec, req, plugin.URL, "tok", "csrf")

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Fatalf("body = %q, want the plugin's JSON body copied verbatim", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
}

func TestProxySettingsNoContentTypeFromPlugin(t *testing.T) {
	// A plugin response with no Content-Type header at all exercises the
	// `if ct != ""` false branch (header not set on w) followed by the
	// non-html io.Copy path.
	plugin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Del("Content-Type")
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("hijack unsupported")
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		defer conn.Close()
		body := "plain body"
		bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: " + itoa(len(body)) + "\r\n\r\n" + body)
		bufrw.Flush()
	}))
	t.Cleanup(plugin.Close)

	req := httptest.NewRequest(http.MethodGet, "/parent/plugins/x/settings", nil)
	rec := httptest.NewRecorder()

	ProxySettings(rec, req, plugin.URL, "tok", "csrf")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "plain body" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
