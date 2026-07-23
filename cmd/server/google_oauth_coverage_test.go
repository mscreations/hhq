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
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"time"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/handlers"
	"github.com/mscreations/hhq/internal/models"
)

const testGoogleOAuthStateTTL = time.Hour

// startFakeGoogleOAuth serves a minimal /token + /userinfo pair standing in
// for Google's real endpoints, redirected to via config.GoogleEndpoint /
// the handlers package's googleUserInfoURL var (both test-only override
// points added specifically so this flow can be exercised end-to-end
// without a real Google account). tokenResp/userinfoStatus let each test
// control the exact response shape.
type fakeGoogleOAuth struct {
	*httptest.Server
	refreshToken string // empty means omit refresh_token from the token response
	userEmail    string
	userinfoErr  bool // true makes the userinfo endpoint 500
}

func startFakeGoogleOAuth(t *testing.T) *fakeGoogleOAuth {
	t.Helper()
	f := &fakeGoogleOAuth{refreshToken: "fake-refresh-token", userEmail: "kid@example.com"}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		}
		if f.refreshToken != "" {
			body["refresh_token"] = f.refreshToken
		}
		json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if f.userinfoErr {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"email": f.userEmail})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// withGoogleEndpoints points config.GoogleEndpoint and the handlers
// package's googleUserInfoURL at fake's URLs for the duration of the test,
// restoring the real values on cleanup so other tests aren't affected
// (these are process-global vars).
func withGoogleEndpoints(t *testing.T, fake *fakeGoogleOAuth) {
	t.Helper()
	origEndpoint := config.GoogleEndpoint
	config.GoogleEndpoint.AuthURL = fake.URL + "/auth"
	config.GoogleEndpoint.TokenURL = fake.URL + "/token"
	t.Cleanup(func() { config.GoogleEndpoint = origEndpoint })

	origUserInfoURL := handlers.GoogleUserInfoURL
	handlers.GoogleUserInfoURL = fake.URL + "/userinfo"
	t.Cleanup(func() { handlers.GoogleUserInfoURL = origUserInfoURL })
}

func TestGoogleConnectCallbackCreatesNewAccountEndToEnd(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Cfg.GoogleOAuthClientID = "test-client-id"
	ts.App.Cfg.GoogleOAuthClientSecret = "test-client-secret"
	ts.login(t, "parent@example.com", "hunter22")

	fake := startFakeGoogleOAuth(t)
	withGoogleEndpoints(t, fake)

	// Mint a real signed state the same way GoogleConnectStart would, for a
	// fresh (non-reconnect) connect.
	state := ts.App.GoogleOAuthState.Sign(0, auth.ActionGoogleOAuthConnect, testGoogleOAuthStateTTL)

	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/google/callback?state=" + url.QueryEscape(state) + "&code=fake-code")
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	if loc := resp.Header.Get("Location"); loc != "/parent" {
		t.Fatalf("Location = %q, want /parent", loc)
	}

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}
	if accounts[0].Provider != models.ProviderGoogle {
		t.Errorf("Provider = %q, want google", accounts[0].Provider)
	}
	if accounts[0].Name != "kid@example.com" {
		t.Errorf("Name = %q, want the fetched email used as the display name", accounts[0].Name)
	}
}

func TestGoogleConnectCallbackReconnectsExistingAccount(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Cfg.GoogleOAuthClientID = "test-client-id"
	ts.App.Cfg.GoogleOAuthClientSecret = "test-client-secret"
	ts.login(t, "parent@example.com", "hunter22")

	id, err := ts.App.CalendarAccounts.CreateGoogle(t.Context(), "Old Name", []byte("old-encrypted"), "old@example.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}

	fake := startFakeGoogleOAuth(t)
	fake.userEmail = "new@example.com"
	withGoogleEndpoints(t, fake)

	state := ts.App.GoogleOAuthState.Sign(id, auth.ActionGoogleOAuthConnect, testGoogleOAuthStateTTL)
	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/google/callback?state=" + url.QueryEscape(state) + "&code=fake-code")
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected the reconnect to update the existing row, not create a new one; got %d accounts", len(accounts))
	}
}

func TestGoogleConnectCallbackMissingCode(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Cfg.GoogleOAuthClientID = "test-client-id"
	ts.App.Cfg.GoogleOAuthClientSecret = "test-client-secret"
	ts.login(t, "parent@example.com", "hunter22")

	state := ts.App.GoogleOAuthState.Sign(0, auth.ActionGoogleOAuthConnect, testGoogleOAuthStateTTL)
	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/google/callback?state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestGoogleConnectCallbackExchangeFailure(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Cfg.GoogleOAuthClientID = "test-client-id"
	ts.App.Cfg.GoogleOAuthClientSecret = "test-client-secret"
	ts.login(t, "parent@example.com", "hunter22")

	// A token endpoint that always 400s stands in for a rejected/expired code.
	badToken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	defer badToken.Close()
	origEndpoint := config.GoogleEndpoint
	config.GoogleEndpoint.AuthURL = badToken.URL + "/auth"
	config.GoogleEndpoint.TokenURL = badToken.URL + "/token"
	t.Cleanup(func() { config.GoogleEndpoint = origEndpoint })

	state := ts.App.GoogleOAuthState.Sign(0, auth.ActionGoogleOAuthConnect, testGoogleOAuthStateTTL)
	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/google/callback?state=" + url.QueryEscape(state) + "&code=bad-code")
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect back to /parent with a friendly error)", resp.StatusCode, http.StatusSeeOther)
	}
	if !strings.HasPrefix(resp.Header.Get("Location"), "/parent?google_error=") {
		t.Fatalf("Location = %q, want a /parent?google_error=... redirect", resp.Header.Get("Location"))
	}
}

func TestGoogleConnectCallbackNoRefreshToken(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Cfg.GoogleOAuthClientID = "test-client-id"
	ts.App.Cfg.GoogleOAuthClientSecret = "test-client-secret"
	ts.login(t, "parent@example.com", "hunter22")

	fake := startFakeGoogleOAuth(t)
	fake.refreshToken = ""
	withGoogleEndpoints(t, fake)

	state := ts.App.GoogleOAuthState.Sign(0, auth.ActionGoogleOAuthConnect, testGoogleOAuthStateTTL)
	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/google/callback?state=" + url.QueryEscape(state) + "&code=fake-code")
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("expected no account created when Google omits a refresh token, got %d", len(accounts))
	}
}

func TestGoogleConnectCallbackUserinfoFailureStillConnects(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Cfg.GoogleOAuthClientID = "test-client-id"
	ts.App.Cfg.GoogleOAuthClientSecret = "test-client-secret"
	ts.login(t, "parent@example.com", "hunter22")

	fake := startFakeGoogleOAuth(t)
	fake.userinfoErr = true
	withGoogleEndpoints(t, fake)

	state := ts.App.GoogleOAuthState.Sign(0, auth.ActionGoogleOAuthConnect, testGoogleOAuthStateTTL)
	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/google/callback?state=" + url.QueryEscape(state) + "&code=fake-code")
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (email lookup failure is non-fatal)", resp.StatusCode, http.StatusSeeOther)
	}

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected the account to still be created despite the userinfo failure, got %d", len(accounts))
	}
	if accounts[0].Name != "Google Calendar" {
		t.Errorf("Name = %q, want the fallback name since email lookup failed", accounts[0].Name)
	}
}

func TestGoogleConnectStartReconnectValidation(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Cfg.GoogleOAuthClientID = "test-client-id"
	ts.App.Cfg.GoogleOAuthClientSecret = "test-client-secret"
	ts.login(t, "parent@example.com", "hunter22")

	t.Run("invalid reconnect_id", func(t *testing.T) {
		resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/google/connect?reconnect_id=not-a-number")
		if err != nil {
			t.Fatalf("GET connect: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
		}
	})

	t.Run("reconnect_id not found", func(t *testing.T) {
		resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/google/connect?reconnect_id=999999")
		if err != nil {
			t.Fatalf("GET connect: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
		}
	})

	t.Run("reconnect_id points at a non-Google account", func(t *testing.T) {
		id, err := ts.App.CalendarAccounts.Create(t.Context(), models.CalendarAccount{
			Name:              "Fastmail",
			Provider:          models.ProviderFastmail,
			CalDAVURL:         sql.NullString{String: "https://caldav.fastmail.com/dav/", Valid: true},
			Username:          sql.NullString{String: "user", Valid: true},
			EncryptedPassword: []byte("enc"),
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		resp, err := ts.Client.Get(fmt.Sprintf("%s/parent/calendar-accounts/google/connect?reconnect_id=%d", ts.URL, id))
		if err != nil {
			t.Fatalf("GET connect: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
		}
	})

	t.Run("reconnect_id points at a valid Google account", func(t *testing.T) {
		id, err := ts.App.CalendarAccounts.CreateGoogle(t.Context(), "Existing", []byte("enc"), "existing@example.com")
		if err != nil {
			t.Fatalf("CreateGoogle: %v", err)
		}
		resp, err := ts.Client.Get(fmt.Sprintf("%s/parent/calendar-accounts/google/connect?reconnect_id=%d", ts.URL, id))
		if err != nil {
			t.Fatalf("GET connect: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusFound)
		}
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatalf("parsing Location: %v", err)
		}
		reconnectID, action, err := ts.App.GoogleOAuthState.Verify(loc.Query().Get("state"))
		if err != nil {
			t.Fatalf("Verify state: %v", err)
		}
		if reconnectID != id {
			t.Errorf("state reconnectID = %d, want %d", reconnectID, id)
		}
		_ = action
	})
}
