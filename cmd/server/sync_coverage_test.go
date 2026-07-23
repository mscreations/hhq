package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/models"
)

// pollUntil polls cond every 20ms until it returns true or deadline elapses,
// failing the test on timeout. Mirrors router_test.go's
// pollUntilWeatherCached but generalized for reuse across sync-related
// coverage tests, several of which need to wait on syncAccountAsync's
// background goroutine to finish before asserting its effect.
func pollUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// --- internal/handlers/sync.go: syncAccountAsync's ProviderPlugin branch + syncPluginAccount ---

// TestResyncPluginAccountSyncsViaPluginPath covers syncAccountAsync's
// ProviderPlugin branch (previously untested - "Resync Now" on a plugin's
// synthetic calendar account had never been driven end to end) and
// syncPluginAccount's happy path, confirming it actually fetches events via
// plugins.SyncOne rather than silently no-oping.
func TestResyncPluginAccountSyncsViaPluginPath(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "resync-plugin@example.com", "s3cret-password")

	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "resync-plugin-token"})
	})
	mux.HandleFunc("/manifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resync-plugin", "name": "Resync Plugin", "version": "1.0.0",
			"view": map[string]any{"enabled": false}, "provides_events": true,
		})
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"events": []map[string]any{
				{
					"uid": "resync-bill-1", "summary": "Resync bill",
					"starts_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
					"ends_at":   time.Now().Add(25 * time.Hour).Format(time.RFC3339),
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "resync-plugin", Name: "Resync Plugin", BaseURL: srv.URL, Enabled: true},
	})
	p, err := ts.App.Plugins.GetByID(t.Context(), "resync-plugin")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	cal, err := ts.App.Calendars.GetByID(t.Context(), int(p.CalendarID.Int32))
	if err != nil {
		t.Fatalf("Calendars.GetByID: %v", err)
	}

	// Clear whatever the initial provisioning-time discovery may have synced
	// so we can positively confirm this resync call is what populates it.
	if err := ts.App.Events.PruneStale(t.Context(), cal.ID, nil); err != nil {
		t.Fatalf("PruneStale: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/calendar-accounts/%d/resync", cal.CalendarAccountID), csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("resync: status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	pollUntil(t, 3*time.Second, func() bool {
		events, err := ts.App.Events.ListInWindow(t.Context(), 7)
		if err != nil {
			return false
		}
		for _, e := range events {
			if e.UID == "resync-bill-1" {
				return true
			}
		}
		return false
	})
}

// TestResyncPluginAccountNoSyntheticCalendarFound covers syncPluginAccount's
// "no synthetic calendar found" branch: a ProviderPlugin calendar_accounts
// row with zero calendars attached (constructed directly, bypassing the
// normal ensurePluginCalendar provisioning flow).
func TestResyncPluginAccountNoSyntheticCalendarFound(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "resync-orphan-account@example.com", "s3cret-password")

	accountID, err := ts.App.CalendarAccounts.Create(t.Context(), models.CalendarAccount{
		Name: "Orphan Plugin Account", Provider: models.ProviderPlugin,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/calendar-accounts/%d/resync", accountID), csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	// Nothing durable to poll for (the branch only logs) - give the
	// background goroutine a moment to run so the log line (and coverage)
	// is actually exercised before the test process exits.
	time.Sleep(150 * time.Millisecond)
}

// TestResyncPluginAccountNoPluginFoundForCalendar covers syncPluginAccount's
// "no plugin found for synthetic calendar" branch: the account has a
// calendar, but no hhq_plugins row references it (constructed directly,
// bypassing ensurePluginCalendar's SetCalendarID step).
func TestResyncPluginAccountNoPluginFoundForCalendar(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "resync-unlinked-account@example.com", "s3cret-password")

	accountID, err := ts.App.CalendarAccounts.Create(t.Context(), models.CalendarAccount{
		Name: "Unlinked Plugin Account", Provider: models.ProviderPlugin,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	color, err := ts.App.Calendars.NextAvailableColor(t.Context())
	if err != nil {
		t.Fatalf("NextAvailableColor: %v", err)
	}
	if _, err := ts.App.Calendars.UpsertDiscovered(t.Context(), accountID, "unlinked-path", "Unlinked", color); err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/calendar-accounts/%d/resync", accountID), csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	time.Sleep(150 * time.Millisecond)
}

// TestResyncAccountNotFoundLogsAndReturns covers syncAccountAsync's initial
// "loading account" error branch: resyncing an id that doesn't exist at all.
func TestResyncAccountNotFoundLogsAndReturns(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "resync-missing-account@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/calendar-accounts/999999/resync", csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	time.Sleep(150 * time.Millisecond)
}

// TestResyncGoogleAccountDecryptRefreshTokenError covers syncAccountAsync's
// ProviderGoogle branch's "decrypting refresh token" error, which must
// record the failure via MarkSynced.
func TestResyncGoogleAccountDecryptRefreshTokenError(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "resync-google-decrypt@example.com", "s3cret-password")

	accountID, err := ts.App.CalendarAccounts.CreateGoogle(t.Context(), "Broken Google Account", []byte("not-valid-ciphertext"), "kid@example.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/calendar-accounts/%d/resync", accountID), csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	pollUntil(t, 3*time.Second, func() bool {
		account, err := ts.App.CalendarAccounts.GetByID(t.Context(), accountID)
		return err == nil && account.LastSyncError.Valid
	})
}

// TestResyncGoogleAccountSyncErrorRecordsFailure covers syncAccountAsync's
// ProviderGoogle branch's SyncGoogleAccount-failed logging + MarkSynced
// call: the refresh token decrypts fine, but the OAuth2 token endpoint
// (redirected to a local server for determinism, rather than depending on
// real network access to Google) rejects the exchange.
func TestResyncGoogleAccountSyncErrorRecordsFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "resync-google-tokenfail@example.com", "s3cret-password")

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	}))
	t.Cleanup(tokenSrv.Close)
	origEndpoint := config.GoogleEndpoint
	config.GoogleEndpoint = oauth2.Endpoint{TokenURL: tokenSrv.URL}
	t.Cleanup(func() { config.GoogleEndpoint = origEndpoint })
	ts.App.Cfg.GoogleOAuthClientID = "test-client-id"
	ts.App.Cfg.GoogleOAuthClientSecret = "test-client-secret"

	encrypted, err := ts.App.Encryptor.Encrypt("fake-refresh-token")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	accountID, err := ts.App.CalendarAccounts.CreateGoogle(t.Context(), "Token Fail Google Account", encrypted, "kid2@example.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/calendar-accounts/%d/resync", accountID), csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	pollUntil(t, 5*time.Second, func() bool {
		account, err := ts.App.CalendarAccounts.GetByID(t.Context(), accountID)
		return err == nil && account.LastSyncError.Valid
	})
}

// --- internal/handlers/sync.go: BootstrapCalendarAccounts remaining branches ---

// TestBootstrapCalendarAccountsSkipsMissingRequiredFields covers the
// "name, username, and password are all required" validation skip.
func TestBootstrapCalendarAccountsSkipsMissingRequiredFields(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapCalendarAccounts(t.Context(), []config.CalendarAccountBootstrap{
		{Name: "No Username", Provider: "fastmail", Password: "pw"},
		{Name: "No Password", Provider: "fastmail", Username: "user"},
		{Provider: "fastmail", Username: "user", Password: "pw"},
	})

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil || len(accounts) != 0 {
		t.Fatalf("expected no accounts created from invalid entries, got accounts=%+v err=%v", accounts, err)
	}
}

// TestBootstrapCalendarAccountsSkipsUnknownProvider covers ResolveProvider's
// error branch.
func TestBootstrapCalendarAccountsSkipsUnknownProvider(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapCalendarAccounts(t.Context(), []config.CalendarAccountBootstrap{
		{Name: "Bad Provider", Provider: "not-a-real-provider", Username: "user", Password: "pw"},
	})

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil || len(accounts) != 0 {
		t.Fatalf("expected no accounts created for an unknown provider, got accounts=%+v err=%v", accounts, err)
	}
}

// TestBootstrapCalendarAccountsSkipsGenericProviderWithoutURL covers
// DefaultCalDAVURL's empty-string branch: a generic-provider entry with no
// explicit url has no well-known default, so it must be skipped.
func TestBootstrapCalendarAccountsSkipsGenericProviderWithoutURL(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapCalendarAccounts(t.Context(), []config.CalendarAccountBootstrap{
		{Name: "No URL Generic", Provider: "generic", Username: "user", Password: "pw"},
	})

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil || len(accounts) != 0 {
		t.Fatalf("expected no account created for generic provider without a url, got accounts=%+v err=%v", accounts, err)
	}
}

// TestBootstrapCalendarAccountsStoreErrorContinuesAndReturnsEarly mirrors
// TestBootstrapPluginsStoreErrorContinuesAndReturnsEarly: a single broken
// CalendarAccounts store swap covers both the per-entry "case err != nil"
// GetByName-failure branch and the trailing removal-reconciliation ListAll
// error-return branch.
func TestBootstrapCalendarAccountsStoreErrorContinuesAndReturnsEarly(t *testing.T) {
	ts := newTestServer(t)
	orig := ts.App.CalendarAccounts
	ts.App.CalendarAccounts = &models.CalendarAccountStore{DB: brokenDB(t)}
	defer func() { ts.App.CalendarAccounts = orig }()

	ts.App.BootstrapCalendarAccounts(t.Context(), []config.CalendarAccountBootstrap{
		{Name: "Unreachable Store", Provider: "fastmail", Username: "user", Password: "pw"},
	})
}
