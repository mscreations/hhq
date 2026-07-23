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
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestGoogleConnectStartNotFoundWhenUnconfigured confirms the "Connect
// Google Calendar" route stays inert (404, not a confusing downstream OAuth
// error) when GOOGLE_OAUTH_CLIENT_ID/SECRET aren't set - the default state
// for any deployment that hasn't opted into Google Calendar sync.
func TestGoogleConnectStartNotFoundWhenUnconfigured(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "parent@example.com", "hunter22")

	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/google/connect")
	if err != nil {
		t.Fatalf("GET connect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// TestGoogleConnectStartRedirectsToGoogleWithSignedState confirms a
// logged-in parent hitting the connect route, once Google OAuth is
// configured, gets redirected straight to Google's consent screen with a
// non-empty, opaque state parameter (the CSRF token verified on callback).
func TestGoogleConnectStartRedirectsToGoogleWithSignedState(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Cfg.GoogleOAuthClientID = "test-client-id"
	ts.App.Cfg.GoogleOAuthClientSecret = "test-client-secret"
	ts.login(t, "parent@example.com", "hunter22")

	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/google/connect")
	if err != nil {
		t.Fatalf("GET connect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusFound)
	}

	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parsing Location header: %v", err)
	}
	if !strings.Contains(loc.Host, "google.com") {
		t.Errorf("redirect host = %q, want a google.com auth endpoint", loc.Host)
	}
	if loc.Query().Get("client_id") != "test-client-id" {
		t.Errorf("client_id = %q, want test-client-id", loc.Query().Get("client_id"))
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("expected a non-empty state parameter")
	}
	if loc.Query().Get("access_type") != "offline" {
		t.Error("expected access_type=offline so a refresh token is actually issued")
	}
}

// TestGoogleConnectCallbackRejectsInvalidState confirms a forged/garbage
// state parameter is rejected with 400 and never creates an account row -
// this is the flow's entire CSRF defense (see google_oauth.go), so a
// regression here would be a real forgeable-connect vulnerability.
func TestGoogleConnectCallbackRejectsInvalidState(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Cfg.GoogleOAuthClientID = "test-client-id"
	ts.App.Cfg.GoogleOAuthClientSecret = "test-client-secret"
	ts.login(t, "parent@example.com", "hunter22")

	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/google/callback?state=not-a-real-token&code=fake-code")
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("expected no account to be created from an invalid-state callback, got %+v", accounts)
	}
}

// TestGoogleConnectCallbackHandlesConsentDenied confirms clicking "Cancel"
// on Google's consent screen (which redirects back with ?error=...) sends
// the parent back to the dashboard with a friendly flash message instead of
// a raw error page.
func TestGoogleConnectCallbackHandlesConsentDenied(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Cfg.GoogleOAuthClientID = "test-client-id"
	ts.App.Cfg.GoogleOAuthClientSecret = "test-client-secret"
	ts.login(t, "parent@example.com", "hunter22")

	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/google/callback?error=access_denied")
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/parent?google_error=") {
		t.Fatalf("Location = %q, want a /parent?google_error=... redirect", loc)
	}
}
