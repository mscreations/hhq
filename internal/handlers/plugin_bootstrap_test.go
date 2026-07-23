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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
	"github.com/mscreations/hhq/internal/util"
)

// TestRetryPluginRegistrationSucceedsOnceTheStartOrderingClears is a direct
// regression test for the background-retry path CLAUDE.md's Round 20 flagged
// as never having an automated test: ensurePluginReady's first attempt
// against a plugin that isn't up yet must not be a fatal failure - it should
// fall back to retryPluginRegistration's ticker and pick the plugin up once
// it becomes reachable, without the caller having to restart hhq.
func TestRetryPluginRegistrationSucceedsOnceTheStartOrderingClears(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	encryptor, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	// Shrink the retry interval for the duration of this test so it doesn't
	// have to wait out the real 15-second production interval.
	orig := pluginRegistrationRetryInterval
	pluginRegistrationRetryInterval = 30 * time.Millisecond
	t.Cleanup(func() { pluginRegistrationRetryInterval = orig })

	var ready atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "retry-test-token"})
	})
	mux.HandleFunc("/manifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "flaky-plugin", "name": "Flaky Plugin", "version": "1.0.0",
			"view":            map[string]any{"enabled": false},
			"provides_events": false,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	plugins := &models.PluginStore{DB: conn}
	a := &App{Plugins: plugins, Encryptor: encryptor}

	if err := plugins.Create(ctx, models.Plugin{
		ID: "flaky-plugin", Name: "Flaky Plugin", BaseURL: srv.URL,
		Enabled: true, BootstrapManaged: true,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// First attempt happens synchronously inside ensurePluginReady and must
	// fail (the plugin isn't "ready" yet), falling back to the background
	// retry goroutine rather than giving up.
	a.ensurePluginReady(ctx, "flaky-plugin", srv.URL)

	p, err := plugins.GetByID(ctx, "flaky-plugin")
	if err != nil {
		t.Fatalf("GetByID after first attempt: %v", err)
	}
	if len(p.EncryptedToken) != 0 {
		t.Fatal("expected no token stored yet - the plugin wasn't reachable on the first attempt")
	}

	// Let the plugin "start" and confirm the background retry picks it up
	// without any further action from the caller.
	ready.Store(true)

	deadline := time.Now().Add(2 * time.Second)
	for {
		p, err := plugins.GetByID(ctx, "flaky-plugin")
		if err != nil {
			t.Fatalf("GetByID while polling: %v", err)
		}
		if len(p.EncryptedToken) > 0 {
			token, err := encryptor.Decrypt(p.EncryptedToken)
			if err != nil {
				t.Fatalf("Decrypt stored token: %v", err)
			}
			if token != "retry-test-token" {
				t.Fatalf("stored token = %q, want %q", token, "retry-test-token")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for retryPluginRegistration to store a token")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
