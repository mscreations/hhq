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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mscreations/hhq/internal/config"
)

// reauthPluginServer is a fake plugin that mimics billtracker-plugin's real
// contract for this feature: POST /register requires a matching
// X-Plugin-Connection-Secret header, always issues a fresh token on a valid
// secret (overwriting whatever it had), and every other route rejects a
// stale/mismatched bearer token with 403 Forbidden - exactly the behavior
// hhq's callWithReauth/retryOnForbidden (internal/handlers/plugin_auth.go)
// is meant to recover from automatically.
func reauthPluginServer(t *testing.T, connectionSecret string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	var currentToken string

	checkAuth := func(w http.ResponseWriter, r *http.Request) bool {
		mu.Lock()
		want := currentToken
		mu.Unlock()
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || want == "" || got != want {
			w.WriteHeader(http.StatusForbidden)
			return false
		}
		return true
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plugin-Connection-Secret") != connectionSecret {
			// Matches the real contract (billtracker-plugin's register.go):
			// a bad connection secret is 401, distinct from the 403 every
			// other route uses for a stale bearer token.
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw := make([]byte, 16)
		_, _ = rand.Read(raw)
		mu.Lock()
		currentToken = hex.EncodeToString(raw)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": currentToken})
	})
	mux.HandleFunc("/manifest", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "reauth-plugin", "name": "Reauth Plugin", "version": "1.0.0",
			"view":            map[string]any{"enabled": true, "label": "Reauth", "icon": ""},
			"provides_events": false,
		})
	})
	mux.HandleFunc("/view", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<p>reauth view content</p>"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestKioskPluginViewReregistersAfterTokenRejected is a direct regression
// test for Part 1's 403-triggers-reauth behavior: if the plugin's stored
// token no longer matches what hhq has (simulated here by re-registering
// directly against the fake server, out from under hhq, so the plugin's
// live token changes without hhq's stored copy changing), the next request
// through hhq must transparently recover by re-registering itself and
// retrying - the kiosk request must still succeed, not show an error state.
func TestKioskPluginViewReregistersAfterTokenRejected(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Cfg.PluginConnectionSecret = "reauth-test-secret"
	plugin := reauthPluginServer(t, "reauth-test-secret")

	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "reauth-plugin", Name: "Reauth Plugin", BaseURL: plugin.URL, Enabled: true},
	})

	// Confirm the happy path works before invalidating anything.
	resp, err := ts.Client.Get(ts.URL + "/kiosk/view/plugin/reauth-plugin")
	if err != nil {
		t.Fatalf("GET (before invalidation): %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "reauth view content") {
		t.Fatalf("expected the real view content before invalidation, got: %s", body)
	}

	// Invalidate hhq's stored token by having the plugin issue a new one
	// directly (bypassing hhq) - this is what a plugin redeploy that loses
	// its token store looks like from hhq's perspective: the token hhq has
	// on file is suddenly rejected.
	registerReq, err := http.NewRequest(http.MethodPost, plugin.URL+"/register", nil)
	if err != nil {
		t.Fatalf("building direct register request: %v", err)
	}
	registerReq.Header.Set("X-Plugin-Connection-Secret", "reauth-test-secret")
	if _, err := http.DefaultClient.Do(registerReq); err != nil {
		t.Fatalf("direct register call: %v", err)
	}

	// hhq's stored token is now stale. The next kiosk request must still
	// succeed - hhq should detect the 403, re-register itself, and retry.
	resp2, err := ts.Client.Get(ts.URL + "/kiosk/view/plugin/reauth-plugin")
	if err != nil {
		t.Fatalf("GET (after invalidation): %v", err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	if !strings.Contains(string(body2), "reauth view content") {
		t.Fatalf("expected hhq to recover by re-registering and retrying, got: %s", body2)
	}
}

// TestKioskPluginViewShowsUnavailableWhenReregistrationFails confirms the
// one-shot nature of the reauth recovery: if the connection secret hhq has
// configured no longer matches the plugin's (so re-registration itself
// fails), the plugin view falls back to the existing "unavailable" state
// rather than looping or erroring the whole page.
func TestKioskPluginViewShowsUnavailableWhenReregistrationFails(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Cfg.PluginConnectionSecret = "reauth-test-secret"
	plugin := reauthPluginServer(t, "reauth-test-secret")

	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "reauth-plugin-2", Name: "Reauth Plugin 2", BaseURL: plugin.URL, Enabled: true},
	})

	// Invalidate the stored token (same technique as above)...
	registerReq, _ := http.NewRequest(http.MethodPost, plugin.URL+"/register", nil)
	registerReq.Header.Set("X-Plugin-Connection-Secret", "reauth-test-secret")
	if _, err := http.DefaultClient.Do(registerReq); err != nil {
		t.Fatalf("direct register call: %v", err)
	}
	// ...then also change hhq's own configured secret so its recovery
	// attempt itself gets rejected.
	ts.App.Cfg.PluginConnectionSecret = "a-different-secret-hhq-now-has"

	resp, err := ts.Client.Get(ts.URL + "/kiosk/view/plugin/reauth-plugin-2")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (kiosk shows an unavailable state, not an error page); body: %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "reauth view content") {
		t.Fatalf("expected the real view content NOT to be shown when re-registration itself fails, got: %s", body)
	}
	if !strings.Contains(string(body), "isn't available right now") {
		t.Fatalf("expected the plugin-unavailable message, got: %s", body)
	}
}
