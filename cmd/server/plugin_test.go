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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/plugins"
)

// fakePluginViewID is the fixed view id fakePluginServer's single registered
// view uses - kiosk routes to a plugin view need both the plugin's own id
// and this view id (e.g. /kiosk/view/plugin/bill-tracker/bills).
const fakePluginViewID = "bills"

// fakePluginServer stands in for an external-process plugin, serving the
// full contract (manifest/view/events/settings/healthz) described in
// internal/plugins' package doc - the same "real fake server, real binary"
// rigor CLAUDE.md used for CalDAV (see caldav_test.go's mock servers). Its
// manifest always reports exactly one view, with the fixed id "bills" (see
// fakePluginViewID) - viewEnabled controls whether that view entry has
// enabled:true/false, matching the pre-multi-view contract's viewEnabled
// param, just via the new views array shape.
func fakePluginServer(t *testing.T, viewEnabled bool, label, icon string, providesEvents bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "fake-plugin-token"})
	})
	mux.HandleFunc("/manifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "bill-tracker",
			"name":    "Bill Tracker",
			"version": "1.0.0",
			"views": []map[string]any{
				{"id": fakePluginViewID, "enabled": viewEnabled, "label": label, "icon": icon},
			},
			"provides_events": providesEvents,
		})
	})
	mux.HandleFunc("/view/{viewID}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<p>3 bills due</p>"))
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"events": []map[string]any{
				{
					"uid":       "electric-bill-1",
					"summary":   "Electric bill due",
					"all_day":   true,
					"starts_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
					"ends_at":   time.Now().Add(48 * time.Hour).Format(time.RFC3339),
				},
			},
		})
	})
	mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<p>plugin settings page</p>"))
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestBootstrapPluginsProvisionsManifestAndSyntheticCalendar exercises
// BootstrapPlugins end-to-end against a fake plugin server and a real,
// migrated Postgres: the plugin row is created, its nav/view shape/
// provides_events flag are cached from a real GET /manifest fetch, and a
// dedicated synthetic calendar_accounts/calendars pair is auto-provisioned
// (see internal/handlers/plugin_bootstrap.go's ensurePluginCalendar).
// Re-running bootstrap must reuse the same calendar rather than
// provisioning a second one.
func TestBootstrapPluginsProvisionsManifestAndSyntheticCalendar(t *testing.T) {
	ts := newTestServer(t)
	plugin := fakePluginServer(t, true, "Bills", "<svg></svg>", true)

	entries := []config.PluginBootstrap{
		{ID: "bill-tracker", Name: "Bill Tracker", BaseURL: plugin.URL, Enabled: true},
	}
	ts.App.BootstrapPlugins(t.Context(), entries)

	p, err := ts.App.Plugins.GetByID(t.Context(), "bill-tracker")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !p.BootstrapManaged || !p.Enabled {
		t.Fatalf("expected a bootstrap-managed, enabled plugin, got %+v", p)
	}
	views, err := ts.App.Plugins.ListViews(t.Context())
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	if len(views) != 1 || views[0].PluginID != "bill-tracker" || views[0].ViewID != fakePluginViewID {
		t.Fatalf("expected exactly one view cached from manifest, got %+v", views)
	}
	if views[0].Label != "Bills" {
		t.Fatalf("expected label=%q cached from manifest, got %+v", "Bills", views[0])
	}
	if views[0].Icon != "<svg></svg>" {
		t.Fatalf("expected icon cached from manifest, got %+v", views[0])
	}
	if !p.ProvidesEvents {
		t.Fatal("expected provides_events=true cached from manifest")
	}
	if !p.CalendarID.Valid {
		t.Fatal("expected a synthetic calendar to be auto-provisioned")
	}

	cal, err := ts.App.Calendars.GetByID(t.Context(), int(p.CalendarID.Int32))
	if err != nil {
		t.Fatalf("Calendars.GetByID: %v", err)
	}
	if cal.Provider != models.ProviderPlugin {
		t.Fatalf("expected the synthetic calendar's account provider to be %q, got %q", models.ProviderPlugin, cal.Provider)
	}

	// Re-running bootstrap must not provision a second synthetic calendar.
	ts.App.BootstrapPlugins(t.Context(), entries)
	p2, err := ts.App.Plugins.GetByID(t.Context(), "bill-tracker")
	if err != nil {
		t.Fatalf("GetByID after re-run: %v", err)
	}
	if p2.CalendarID.Int32 != p.CalendarID.Int32 {
		t.Fatalf("expected the same synthetic calendar to be reused, got %d want %d", p2.CalendarID.Int32, p.CalendarID.Int32)
	}
}

// TestKioskPluginViewServesViewHTML confirms the kiosk's plugin nav-button
// endpoint proxies GET /view straight through, wrapped in the
// kiosk-view-page container - the browser never talks to the plugin process
// directly (see internal/plugins' package doc).
func TestKioskPluginViewServesViewHTML(t *testing.T) {
	ts := newTestServer(t)
	plugin := fakePluginServer(t, true, "Bills", "", false)
	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "bill-tracker", Name: "Bill Tracker", BaseURL: plugin.URL, Enabled: true},
	})

	resp, err := ts.Client.Get(ts.URL + "/kiosk/view/plugin/bill-tracker/" + fakePluginViewID)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<p>3 bills due</p>") {
		t.Fatalf("unexpected view body: %q", body)
	}
}

// TestPluginSettingsPageProxiesAndRequiresLogin confirms the settings page
// is only reachable to a logged-in parent (hhq's own auth, not the
// plugin's) and that the proxied response is the plugin's real HTML.
func TestPluginSettingsPageProxiesAndRequiresLogin(t *testing.T) {
	ts := newTestServer(t)
	plugin := fakePluginServer(t, true, "Bills", "", false)
	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "bill-tracker", Name: "Bill Tracker", BaseURL: plugin.URL, Enabled: true},
	})

	resp, err := ts.Client.Get(ts.URL + "/parent/plugins/bill-tracker/settings")
	if err != nil {
		t.Fatalf("GET (unauthenticated): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unauthenticated: status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	ts.login(t, "plugin-admin@example.com", "s3cret-password")
	resp2, err := ts.Client.Get(ts.URL + "/parent/plugins/bill-tracker/settings")
	if err != nil {
		t.Fatalf("GET (authenticated): %v", err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	// The plugin's real content survives, plus hhq's own injected "back to
	// dashboard" link (see internal/plugins.ProxySettings' injectBackLink) -
	// not an exact match anymore now that the proxy augments the HTML.
	if !strings.Contains(string(body), "<p>plugin settings page</p>") {
		t.Fatalf("unexpected proxied body: %q", body)
	}
	if !strings.Contains(string(body), `href="/parent"`) {
		t.Fatalf("expected a back-to-dashboard link injected into the proxied page, got: %q", body)
	}
}

// TestTogglePlugin mirrors TestToggleCalendarEnabled's pattern.
func TestTogglePlugin(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "toggle-plugin@example.com", "s3cret-password")
	plugin := fakePluginServer(t, true, "Bills", "", false)
	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "bill-tracker", Name: "Bill Tracker", BaseURL: plugin.URL, Enabled: true},
	})

	resp := ts.postForm(t, "/parent/plugins/bill-tracker/toggle", csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	p, err := ts.App.Plugins.GetByID(t.Context(), "bill-tracker")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if p.Enabled {
		t.Fatal("expected toggling an enabled plugin to disable it")
	}
}

// TestParentDashboardShowsPluginUpdateIcon confirms the Plugins card renders
// an update-available icon next to a plugin's version when the scheduler's
// PluginVersions cache (see internal/scheduler's checkPluginVersions) has an
// upgrade-available response cached for it (as a plugin's own GET /version
// would report), and that the icon is absent when nothing's cached or the
// cached response says no upgrade is available.
func TestParentDashboardShowsPluginUpdateIcon(t *testing.T) {
	ts := newTestServer(t)
	ts.App.PluginVersions = &plugins.VersionCache{}
	_ = ts.login(t, "plugin-update-icon@example.com", "s3cret-password")

	plugin := fakePluginServer(t, true, "Bills", "", false)
	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "bill-tracker", Name: "Bill Tracker", BaseURL: plugin.URL, Enabled: true},
	})

	ts.App.PluginVersions.Set("bill-tracker", &plugins.VersionInfo{
		Version:          "1.0.0",
		UpgradeAvailable: true,
		UpgradeVersion:   "1.1.0",
		Changelog:        "feat: new stuff",
		Channel:          "release",
	})

	page, err := ts.Client.Get(ts.URL + "/parent")
	if err != nil {
		t.Fatalf("GET /parent: %v", err)
	}
	defer page.Body.Close()
	body, err := io.ReadAll(page.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	html := string(body)
	if !strings.Contains(html, `class="plugin-update-icon"`) {
		t.Fatal("expected the dashboard to show a plugin-update-icon when the cached /version response reports an upgrade")
	}
	if !strings.Contains(html, "1.1.0") || !strings.Contains(html, "feat: new stuff") {
		t.Fatal("expected the update icon's tooltip to include the upgrade version and changelog")
	}
	if strings.Contains(html, `<a class="plugin-update-icon"`) {
		t.Fatal("did not expect the update icon to be a link - GET /version carries no URL")
	}

	// Now cache a response reporting no upgrade available - the icon must
	// not appear.
	ts.App.PluginVersions.Set("bill-tracker", &plugins.VersionInfo{Version: "1.0.0", UpgradeAvailable: false})
	page2, err := ts.Client.Get(ts.URL + "/parent")
	if err != nil {
		t.Fatalf("GET /parent (up to date): %v", err)
	}
	defer page2.Body.Close()
	body2, err := io.ReadAll(page2.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if strings.Contains(string(body2), `class="plugin-update-icon"`) {
		t.Fatal("did not expect an update icon when the cached response reports no upgrade available")
	}
}

var csrfInputRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// TestPluginSettingsPageSubmitRoundTripsCSRFToken is a regression test: a
// plugin's own settings HTML knows nothing of hhq's session/CSRF scheme
// (see internal/plugins.ProxySettings' doc comment), so without
// ProxySettings stamping a token into the plugin's POST form on the way
// out, every submission was rejected by VerifyCSRF with "invalid or
// missing CSRF token" before it ever reached the plugin. This drives the
// exact sequence a real browser does - GET the proxied page, read the
// csrf_token out of the actual rendered HTML, submit it back - rather than
// using the test suite's postForm helper (which attaches a known-good
// token directly and would pass even if injection were broken).
func TestPluginSettingsPageSubmitRoundTripsCSRFToken(t *testing.T) {
	ts := newTestServer(t)
	var gotCSRFToken string
	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "fake-plugin-token"})
	})
	mux.HandleFunc("/manifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "bill-tracker", "name": "Bill Tracker", "version": "1.0.0",
			"views": []map[string]any{{"id": "bills", "enabled": false}}, "provides_events": false,
		})
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	settingsPageHTML := `<form method="POST" action=""><input type="hidden" name="action" value="noop"><button type="submit">Go</button></form>`
	mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodPost {
			gotCSRFToken = r.FormValue("csrf_token")
		}
		// Mirrors the real plugin's SettingsPage handler: POST re-renders the
		// same full settings page (with its own forms) rather than a bare ack.
		_, _ = w.Write([]byte(settingsPageHTML))
	})
	plugin := httptest.NewServer(mux)
	t.Cleanup(plugin.Close)
	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "bill-tracker", Name: "Bill Tracker", BaseURL: plugin.URL, Enabled: true},
	})
	ts.login(t, "plugin-csrf@example.com", "s3cret-password")

	resp, err := ts.Client.Get(ts.URL + "/parent/plugins/bill-tracker/settings")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	m := csrfInputRe.FindStringSubmatch(string(body))
	if m == nil {
		t.Fatalf("expected a csrf_token hidden input injected into the proxied form, got body: %s", body)
	}
	token := m[1]

	postResp, err := ts.Client.PostForm(ts.URL+"/parent/plugins/bill-tracker/settings", url.Values{
		"action":     {"noop"},
		"csrf_token": {token},
	})
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer postResp.Body.Close()
	postBody, _ := io.ReadAll(postResp.Body)
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", postResp.StatusCode, postBody)
	}
	if gotCSRFToken != token {
		t.Fatalf("plugin received csrf_token=%q, want %q (forwarded through as an ordinary form field)", gotCSRFToken, token)
	}

	// Regression case reported after the first fix: the page a POST action
	// responds with (e.g. right after connecting SimpleFIN) must also carry
	// a fresh token, not just the initial GET - otherwise the very next
	// button click on that page (e.g. "Refresh now") hits the same CSRF
	// error without ever navigating away.
	m2 := csrfInputRe.FindStringSubmatch(string(postBody))
	if m2 == nil {
		t.Fatalf("expected a csrf_token hidden input injected into the POST-rendered page too, got body: %s", postBody)
	}
	token2 := m2[1]

	postResp2, err := ts.Client.PostForm(ts.URL+"/parent/plugins/bill-tracker/settings", url.Values{
		"action":     {"noop"},
		"csrf_token": {token2},
	})
	if err != nil {
		t.Fatalf("second POST: %v", err)
	}
	defer postResp2.Body.Close()
	if postResp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(postResp2.Body)
		t.Fatalf("second POST (using token from the first POST's rendered page): status = %d, want 200; body: %s", postResp2.StatusCode, body)
	}
}
