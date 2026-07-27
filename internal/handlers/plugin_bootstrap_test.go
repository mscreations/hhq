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
	"context"
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

// TestCleanupOrphanedPluginCalendarsRemovesAccountWithNoOwningPlugin covers
// the "legitimately removed plugin" case: BootstrapPlugins' own removal loop
// deletes the plugin row for an entry dropped from plugins.json, but that
// deletion does not cascade to the plugin's synthetic calendar_accounts row
// (hhq_plugins.calendar_id is ON DELETE SET NULL, not the reverse), so
// nothing else would ever clean it up without this function.
func TestCleanupOrphanedPluginCalendarsRemovesAccountWithNoOwningPlugin(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	calendarAccounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	plugins := &models.PluginStore{DB: conn}
	a := &App{CalendarAccounts: calendarAccounts, Calendars: calendars, Plugins: plugins}

	accountID, err := calendarAccounts.Create(ctx, models.CalendarAccount{
		Name: "Plugin: Orphaned", Provider: models.ProviderPlugin, BootstrapManaged: true,
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}
	if _, err := calendars.UpsertDiscovered(ctx, accountID, "orphaned-plugin", "Orphaned", "#ff0000"); err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}
	// No plugin row created at all - simulates one already removed by
	// BootstrapPlugins' own removal loop.

	a.CleanupOrphanedPluginCalendars(ctx)

	if _, err := calendarAccounts.GetByID(ctx, accountID); err == nil {
		t.Fatal("expected orphaned plugin calendar account to be removed")
	}
}

// TestCleanupOrphanedPluginCalendarsRemovesNeverConnectedPlugin covers a
// plugin that's still present in plugins.json but has never once completed a
// successful manifest fetch (LastHealthyAt unset) - e.g. permanently
// misconfigured or offline, not just slow to start.
func TestCleanupOrphanedPluginCalendarsRemovesNeverConnectedPlugin(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	calendarAccounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	plugins := &models.PluginStore{DB: conn}
	a := &App{CalendarAccounts: calendarAccounts, Calendars: calendars, Plugins: plugins}

	if err := plugins.Create(ctx, models.Plugin{
		ID: "never-connected", Name: "Never Connected", BaseURL: "http://example.invalid",
		Enabled: true, BootstrapManaged: true,
	}); err != nil {
		t.Fatalf("Create plugin: %v", err)
	}

	accountID, err := calendarAccounts.Create(ctx, models.CalendarAccount{
		Name: "Plugin: Never Connected", Provider: models.ProviderPlugin, BootstrapManaged: true,
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}
	calendarID, err := calendars.UpsertDiscovered(ctx, accountID, "never-connected", "Never Connected", "#ff0000")
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}
	if err := plugins.SetCalendarID(ctx, "never-connected", calendarID); err != nil {
		t.Fatalf("SetCalendarID: %v", err)
	}
	// Deliberately never call MarkHealth - LastHealthyAt stays unset,
	// simulating a plugin that's been retried repeatedly but has never
	// actually answered a manifest request successfully.

	a.CleanupOrphanedPluginCalendars(ctx)

	if _, err := calendarAccounts.GetByID(ctx, accountID); err == nil {
		t.Fatal("expected never-connected plugin's calendar account to be removed")
	}
}

// TestCleanupOrphanedPluginCalendarsKeepsConnectedPlugin is the negative
// case: a plugin that has connected at least once (LastHealthyAt set) must
// survive the cleanup pass regardless of how long ago that was - this is the
// exact scenario the immediate at-boot deletion this function replaces used
// to break (see BootstrapCalendarAccounts).
func TestCleanupOrphanedPluginCalendarsKeepsConnectedPlugin(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	calendarAccounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	plugins := &models.PluginStore{DB: conn}
	a := &App{CalendarAccounts: calendarAccounts, Calendars: calendars, Plugins: plugins}

	if err := plugins.Create(ctx, models.Plugin{
		ID: "healthy-plugin", Name: "Healthy Plugin", BaseURL: "http://example.invalid",
		Enabled: true, BootstrapManaged: true,
	}); err != nil {
		t.Fatalf("Create plugin: %v", err)
	}

	accountID, err := calendarAccounts.Create(ctx, models.CalendarAccount{
		Name: "Plugin: Healthy", Provider: models.ProviderPlugin, BootstrapManaged: true,
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}
	calendarID, err := calendars.UpsertDiscovered(ctx, accountID, "healthy-plugin", "Healthy", "#00ff00")
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}
	if err := plugins.SetCalendarID(ctx, "healthy-plugin", calendarID); err != nil {
		t.Fatalf("SetCalendarID: %v", err)
	}
	if err := plugins.MarkHealth(ctx, "healthy-plugin", nil); err != nil {
		t.Fatalf("MarkHealth: %v", err)
	}

	a.CleanupOrphanedPluginCalendars(ctx)

	if _, err := calendarAccounts.GetByID(ctx, accountID); err != nil {
		t.Fatalf("expected connected plugin's calendar account to survive: %v", err)
	}
}

// TestSchedulePluginCalendarCleanupRunsAfterGracePeriodAndRespectsShutdown
// exercises the actual scheduling wrapper: the check must not run before the
// grace period elapses, must run once it does, and must never fire at all if
// ctx is cancelled first (app shutdown before the grace period elapses).
func TestSchedulePluginCalendarCleanupRunsAfterGracePeriodAndRespectsShutdown(t *testing.T) {
	conn := testutil.RequireDB(t)

	orig := pluginCalendarCleanupGracePeriod
	t.Cleanup(func() { pluginCalendarCleanupGracePeriod = orig })

	calendarAccounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	plugins := &models.PluginStore{DB: conn}
	a := &App{CalendarAccounts: calendarAccounts, Calendars: calendars, Plugins: plugins}

	accountID, err := calendarAccounts.Create(t.Context(), models.CalendarAccount{
		Name: "Plugin: Scheduled", Provider: models.ProviderPlugin, BootstrapManaged: true,
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}
	if _, err := calendars.UpsertDiscovered(t.Context(), accountID, "scheduled-plugin", "Scheduled", "#0000ff"); err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}

	// First: cancel the context before the grace period elapses and confirm
	// the account is never touched.
	pluginCalendarCleanupGracePeriod = 200 * time.Millisecond
	cancelledCtx, cancel := context.WithCancel(context.Background())
	a.SchedulePluginCalendarCleanup(cancelledCtx)
	cancel()
	time.Sleep(400 * time.Millisecond)
	if _, err := calendarAccounts.GetByID(t.Context(), accountID); err != nil {
		t.Fatalf("account should survive a cancelled schedule: %v", err)
	}

	// Then: let it actually run and confirm it fires and removes the
	// still-orphaned account.
	pluginCalendarCleanupGracePeriod = 30 * time.Millisecond
	a.SchedulePluginCalendarCleanup(t.Context())

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := calendarAccounts.GetByID(t.Context(), accountID); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for scheduled cleanup to remove the account")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
