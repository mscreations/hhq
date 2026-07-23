package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/models"
)

// --- internal/handlers/plugin_bootstrap.go: BootstrapPlugins reconciliation branches ---

// TestBootstrapPluginsSkipsInvalidEntries covers the "id and base_url are
// both required" validation skip (missing id, missing base_url) - neither
// entry should ever reach a.Plugins.Create.
func TestBootstrapPluginsSkipsInvalidEntries(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "", BaseURL: "http://example.com"},
		{ID: "missing-url", BaseURL: ""},
	})

	all, err := ts.App.Plugins.ListAll(t.Context())
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected no plugins created from invalid entries, got %+v", all)
	}
}

// TestBootstrapPluginsSkipsNameCollisionWithManualPlugin mirrors the
// analogous calendar-account/child/chore collision tests: a plugin id that
// already exists but wasn't created by bootstrap must be left untouched.
func TestBootstrapPluginsSkipsNameCollisionWithManualPlugin(t *testing.T) {
	ts := newTestServer(t)

	if err := ts.App.Plugins.Create(t.Context(), models.Plugin{
		ID: "shared-plugin", Name: "Manually Added", BaseURL: "http://manual.example.com",
		Enabled: true, BootstrapManaged: false,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "shared-plugin", Name: "Hijacked", BaseURL: "http://hijacked.example.com", Enabled: true},
	})

	unchanged, err := ts.App.Plugins.GetByID(t.Context(), "shared-plugin")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if unchanged.BootstrapManaged || unchanged.Name != "Manually Added" || unchanged.BaseURL != "http://manual.example.com" {
		t.Fatalf("expected the manually-created plugin to be left untouched, got %+v", unchanged)
	}
}

// TestBootstrapPluginsRemovesEntryDroppedFromConfig covers the removal
// reconciliation loop: a bootstrap-managed plugin no longer listed must be
// deleted, mirroring TestBootstrapCalendarAccountsRemovesEntryDroppedFromConfig.
func TestBootstrapPluginsRemovesEntryDroppedFromConfig(t *testing.T) {
	ts := newTestServer(t)
	keep := fakePluginServer(t, false, "", "", false)
	drop := fakePluginServer(t, false, "", "", false)

	entries := []config.PluginBootstrap{
		{ID: "keep-plugin", BaseURL: keep.URL, Enabled: true},
		{ID: "drop-plugin", BaseURL: drop.URL, Enabled: true},
	}
	ts.App.BootstrapPlugins(t.Context(), entries)

	all, err := ts.App.Plugins.ListAll(t.Context())
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAll after first run: all=%+v err=%v", all, err)
	}

	ts.App.BootstrapPlugins(t.Context(), entries[:1])

	allAfter, err := ts.App.Plugins.ListAll(t.Context())
	if err != nil || len(allAfter) != 1 {
		t.Fatalf("ListAll after removal run: allAfter=%+v err=%v", allAfter, err)
	}
	if allAfter[0].ID != "keep-plugin" {
		t.Fatalf("expected only %q to remain, got %+v", "keep-plugin", allAfter)
	}
	if _, err := ts.App.Plugins.GetByID(t.Context(), "drop-plugin"); err != models.ErrNotFound {
		t.Fatalf("GetByID(drop-plugin) err = %v, want ErrNotFound", err)
	}
}

// TestBootstrapPluginsDisabledEntryDoesNotRegister covers the "if e.Enabled"
// false branch: a disabled plugin entry must never trigger ensurePluginReady
// (no self-registration attempt), so /register on the fake server is never
// hit.
func TestBootstrapPluginsDisabledEntryDoesNotRegister(t *testing.T) {
	ts := newTestServer(t)
	var registerHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		registerHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "should-not-be-used"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "disabled-plugin", BaseURL: srv.URL, Enabled: false},
	})

	p, err := ts.App.Plugins.GetByID(t.Context(), "disabled-plugin")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if p.Enabled {
		t.Fatal("expected the plugin to be created disabled")
	}
	if len(p.EncryptedToken) != 0 {
		t.Fatal("expected no token to be stored for a disabled plugin")
	}
	if registerHits.Load() != 0 {
		t.Fatalf("expected /register to never be called for a disabled plugin, got %d hits", registerHits.Load())
	}
}

// TestBootstrapPluginsStoreErrorContinuesAndReturnsEarly covers two branches
// at once via a single broken Plugins store swap: the per-entry "case err !=
// nil" lookup-failure branch (a.Plugins.GetByID returning a non-ErrNotFound
// error) and, since the same broken store is used for the final removal-
// reconciliation ListAll call, that error-return branch too. A single field
// swap can exercise both since neither call needs to have succeeded first -
// unlike the Create/UpdateBootstrap error branches (which need a prior call
// on the same store to have succeeded), documented separately as infeasible.
func TestBootstrapPluginsStoreErrorContinuesAndReturnsEarly(t *testing.T) {
	ts := newTestServer(t)
	orig := ts.App.Plugins
	ts.App.Plugins = &models.PluginStore{DB: brokenDB(t)}
	defer func() { ts.App.Plugins = orig }()

	// Must not panic; both the per-entry lookup and the trailing
	// ListAll-for-removal call fail against the broken store.
	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "unreachable-store-plugin", BaseURL: "http://example.com", Enabled: true},
	})
}

// TestBootstrapPluginsDecryptStoredTokenErrorStopsRetrying covers
// tryRegisterAndRefresh's "not a transient error - retrying won't help"
// branch: a plugin that already has a stored token that fails to decrypt
// (e.g. corrupted, or encrypted under a different ENCRYPTION_KEY) must not
// spawn a background retry loop.
func TestBootstrapPluginsDecryptStoredTokenErrorStopsRetrying(t *testing.T) {
	ts := newTestServer(t)
	plugin := fakePluginServer(t, false, "", "", false)

	if err := ts.App.Plugins.Create(t.Context(), models.Plugin{
		ID: "bad-token-plugin", Name: "Bad Token", BaseURL: plugin.URL,
		Enabled: true, BootstrapManaged: true,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := ts.App.Plugins.SetToken(t.Context(), "bad-token-plugin", []byte("not-valid-ciphertext")); err != nil {
		t.Fatalf("SetToken: %v", err)
	}

	// Should return promptly (no 15s retry loop) since decrypt failure is
	// treated as permanent.
	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "bad-token-plugin", BaseURL: plugin.URL, Enabled: true},
	})
}

// TestBootstrapPluginsProvisioningFailsWhenCalendarAccountsStoreBroken covers
// ensurePluginCalendar's "creating synthetic calendar account" error branch:
// a.CalendarAccounts.Create failing while a.Plugins itself stays healthy
// (a different store, so a single swap isolates exactly this call without
// breaking the preceding a.Plugins.GetByID inside ensurePluginCalendar).
func TestBootstrapPluginsProvisioningFailsWhenCalendarAccountsStoreBroken(t *testing.T) {
	ts := newTestServer(t)
	plugin := fakePluginServer(t, false, "", "", true) // provides_events=true triggers ensurePluginCalendar

	orig := ts.App.CalendarAccounts
	ts.App.CalendarAccounts = &models.CalendarAccountStore{DB: brokenDB(t)}
	defer func() { ts.App.CalendarAccounts = orig }()

	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "provisioning-fail-plugin", BaseURL: plugin.URL, Enabled: true},
	})

	p, err := ts.App.Plugins.GetByID(t.Context(), "provisioning-fail-plugin")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if p.CalendarID.Valid {
		t.Fatal("expected no synthetic calendar to be provisioned when CalendarAccounts.Create fails")
	}
	if !p.ProvidesEvents {
		t.Fatal("expected the manifest fetch itself to have succeeded and been cached")
	}
}

// TestBootstrapPluginsProvisioningFailsWhenCalendarsColorPickFails covers
// ensurePluginCalendar's "picking synthetic calendar color" error branch:
// a.Calendars.NextAvailableColor failing while a.CalendarAccounts (a
// different store) succeeds.
func TestBootstrapPluginsProvisioningFailsWhenCalendarsColorPickFails(t *testing.T) {
	ts := newTestServer(t)
	plugin := fakePluginServer(t, false, "", "", true)

	orig := ts.App.Calendars
	ts.App.Calendars = &models.CalendarStore{DB: brokenDB(t)}
	defer func() { ts.App.Calendars = orig }()

	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "color-fail-plugin", BaseURL: plugin.URL, Enabled: true},
	})

	p, err := ts.App.Plugins.GetByID(t.Context(), "color-fail-plugin")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if p.CalendarID.Valid {
		t.Fatal("expected no synthetic calendar to be provisioned when NextAvailableColor fails")
	}
}

// TestBootstrapPluginsManifestFetchFails covers refreshPluginManifest's own
// "fetching manifest failed" branch: a plugin that self-registers
// successfully (POST /register works) but whose GET /manifest 404s.
func TestBootstrapPluginsManifestFetchFails(t *testing.T) {
	ts := newTestServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "tok"})
	})
	mux.HandleFunc("/manifest", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "bad-manifest-plugin", BaseURL: srv.URL, Enabled: true},
	})

	p, err := ts.App.Plugins.GetByID(t.Context(), "bad-manifest-plugin")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(p.EncryptedToken) == 0 {
		t.Fatal("expected self-registration to have succeeded even though the manifest fetch failed")
	}
	if !p.LastError.Valid {
		t.Fatal("expected MarkHealth to have recorded the manifest fetch failure")
	}
}

// --- internal/handlers/plugins.go ---

// TestBuildPluginNavItemsHappyPathViaKioskIndex covers buildPluginNavItems'
// normal (non-error) path by rendering the real kiosk index page with an
// enabled, view-producing plugin registered.
func TestBuildPluginNavItemsHappyPathViaKioskIndex(t *testing.T) {
	ts := newTestServer(t)
	plugin := fakePluginServer(t, true, "Bills", "<svg>icon</svg>", false)
	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "nav-plugin", Name: "Nav Plugin", BaseURL: plugin.URL, Enabled: true},
	})

	resp, err := ts.Client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `data-nav="plugin-nav-plugin"`) {
		t.Fatalf("expected the plugin's nav button in the rendered kiosk page, got: %s", body)
	}
	if !strings.Contains(string(body), "<svg>icon</svg>") {
		t.Fatalf("expected the plugin's icon HTML rendered verbatim, got: %s", body)
	}
}

// TestBuildPluginNavItemsListViewsErrorReturnsNil covers buildPluginNavItems'
// error branch: a.Plugins.ListViews failing must not fail the whole kiosk
// page load, just omit the plugin nav bar.
func TestBuildPluginNavItemsListViewsErrorReturnsNil(t *testing.T) {
	ts := newTestServer(t)
	orig := ts.App.Plugins
	ts.App.Plugins = &models.PluginStore{DB: brokenDB(t)}
	defer func() { ts.App.Plugins = orig }()

	resp, err := ts.Client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (kiosk page must still render); body: %s", resp.StatusCode, body)
	}
}

// TestKioskPluginViewDecryptTokenError covers KioskPluginView's "decrypting
// token" error branch: an enabled, view-enabled plugin whose stored token
// isn't valid ciphertext must render the empty fragment rather than erroring.
func TestKioskPluginViewDecryptTokenError(t *testing.T) {
	ts := newTestServer(t)
	plugin := fakePluginServer(t, true, "Bills", "", false)
	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "bad-token-view-plugin", BaseURL: plugin.URL, Enabled: true},
	})
	if err := ts.App.Plugins.SetToken(t.Context(), "bad-token-view-plugin", []byte("garbage")); err != nil {
		t.Fatalf("SetToken: %v", err)
	}

	resp, err := ts.Client.Get(ts.URL + "/kiosk/view/plugin/bad-token-view-plugin")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "bills due") {
		t.Fatalf("expected an empty fragment on decrypt failure, got: %s", body)
	}
}

// --- KioskEventAction (0% coverage before this file) ---

// pluginCalendarFixture bootstraps a provides_events plugin, returning its
// synthetic calendar id and the fake plugin server (extended with an
// /actions/{id} handler, which fakePluginServer itself doesn't provide).
type actionPluginFixture struct {
	calendarID int
	pluginID   string
	server     *httptest.Server
	actionHits *atomic.Int32
}

func newActionPluginFixture(t *testing.T, ts *testServer, actionStatus int) actionPluginFixture {
	t.Helper()
	var actionHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "action-plugin-token"})
	})
	mux.HandleFunc("/manifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "action-plugin", "name": "Action Plugin", "version": "1.0.0",
			"view":            map[string]any{"enabled": false},
			"provides_events": true,
		})
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"events": []map[string]any{}})
	})
	mux.HandleFunc("POST /actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		actionHits.Add(1)
		w.WriteHeader(actionStatus)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "action-plugin", Name: "Action Plugin", BaseURL: srv.URL, Enabled: true},
	})
	p, err := ts.App.Plugins.GetByID(t.Context(), "action-plugin")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !p.CalendarID.Valid {
		t.Fatalf("expected a synthetic calendar to be provisioned, got %+v", p)
	}
	return actionPluginFixture{calendarID: int(p.CalendarID.Int32), pluginID: "action-plugin", server: srv, actionHits: &actionHits}
}

func (f actionPluginFixture) hits() int32 { return f.actionHits.Load() }

// TestKioskEventActionSucceeds drives KioskEventAction end to end: a synthetic
// event with a non-parent-required action, tapped without login, POSTs to
// the plugin's /actions/{id} and triggers a resync.
func TestKioskEventActionSucceeds(t *testing.T) {
	ts := newTestServer(t)
	fixture := newActionPluginFixture(t, ts, http.StatusOK)

	if err := ts.App.Events.Upsert(t.Context(), models.Event{
		CalendarID: fixture.calendarID,
		UID:        "bill-1",
		Summary:    "Electric bill",
		Actions:    []models.EventAction{{ID: "mark-paid", Label: "Mark Paid"}},
		StartsAt:   time.Now(),
		EndsAt:     time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	events, err := ts.App.Events.ListInWindow(t.Context(), 7)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListInWindow: events=%+v err=%v", events, err)
	}
	eventID := events[0].ID

	resp, err := ts.Client.Post(
		ts.URL+"/kiosk/events/"+strconv.Itoa(eventID)+"/actions/mark-paid",
		"application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if resp.Header.Get("HX-Trigger") != "eventActionDone" {
		t.Fatalf("HX-Trigger = %q, want %q", resp.Header.Get("HX-Trigger"), "eventActionDone")
	}
	if fixture.hits() != 1 {
		t.Fatalf("expected the plugin's /actions/mark-paid to be hit once, got %d", fixture.hits())
	}
}

// TestKioskEventActionRequiresParentEnforced covers the RequiresParent guard:
// unauthenticated must be forbidden, and logged-in-as-parent must succeed.
func TestKioskEventActionRequiresParentEnforced(t *testing.T) {
	ts := newTestServer(t)
	fixture := newActionPluginFixture(t, ts, http.StatusOK)

	if err := ts.App.Events.Upsert(t.Context(), models.Event{
		CalendarID: fixture.calendarID,
		UID:        "bill-2",
		Summary:    "Water bill",
		Actions:    []models.EventAction{{ID: "mark-paid", Label: "Mark Paid", RequiresParent: true}},
		StartsAt:   time.Now(),
		EndsAt:     time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	events, err := ts.App.Events.ListInWindow(t.Context(), 7)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListInWindow: events=%+v err=%v", events, err)
	}
	eventID := events[0].ID
	path := ts.URL + "/kiosk/events/" + strconv.Itoa(eventID) + "/actions/mark-paid"

	resp, err := ts.Client.Post(path, "application/json", nil)
	if err != nil {
		t.Fatalf("POST (unauthenticated): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated: status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}

	ts.login(t, "action-parent@example.com", "s3cret-password")
	resp2, err := ts.Client.Post(path, "application/json", nil)
	if err != nil {
		t.Fatalf("POST (authenticated): %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("authenticated: status = %d, want 200; body: %s", resp2.StatusCode, body)
	}
	if fixture.hits() != 1 {
		t.Fatalf("expected exactly one successful action call, got %d", fixture.hits())
	}
}

// TestKioskEventActionInvalidEventID covers the parseIDParam failure branch.
func TestKioskEventActionInvalidEventID(t *testing.T) {
	ts := newTestServer(t)
	resp, err := ts.Client.Post(ts.URL+"/kiosk/events/not-a-number/actions/whatever", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestKioskEventActionEventNotFound covers Events.GetByID's ErrNotFound
// branch.
func TestKioskEventActionEventNotFound(t *testing.T) {
	ts := newTestServer(t)
	resp, err := ts.Client.Post(ts.URL+"/kiosk/events/999999/actions/whatever", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// TestKioskEventActionUnknownActionID covers the "unknown action for this
// event" branch: the event exists and has actions, but not the one requested.
func TestKioskEventActionUnknownActionID(t *testing.T) {
	ts := newTestServer(t)
	fixture := newActionPluginFixture(t, ts, http.StatusOK)

	if err := ts.App.Events.Upsert(t.Context(), models.Event{
		CalendarID: fixture.calendarID,
		UID:        "bill-3",
		Summary:    "Gas bill",
		Actions:    []models.EventAction{{ID: "mark-paid", Label: "Mark Paid"}},
		StartsAt:   time.Now(),
		EndsAt:     time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	events, err := ts.App.Events.ListInWindow(t.Context(), 7)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListInWindow: events=%+v err=%v", events, err)
	}

	resp, err := ts.Client.Post(ts.URL+"/kiosk/events/"+strconv.Itoa(events[0].ID)+"/actions/no-such-action", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestKioskEventActionPluginNotFoundForCalendar covers
// Plugins.GetByCalendarID's ErrNotFound branch: an event whose calendar
// isn't a plugin-owned synthetic calendar (e.g. a plain bootstrap-created
// calendar with no owning plugin row).
func TestKioskEventActionPluginNotFoundForCalendar(t *testing.T) {
	ts := newTestServer(t)

	accountID, err := ts.App.CalendarAccounts.Create(t.Context(), models.CalendarAccount{
		Name: "Orphan Account", Provider: models.ProviderGeneric,
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}
	color, err := ts.App.Calendars.NextAvailableColor(t.Context())
	if err != nil {
		t.Fatalf("NextAvailableColor: %v", err)
	}
	calID, err := ts.App.Calendars.UpsertDiscovered(t.Context(), accountID, "orphan-path", "Orphan", color)
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}
	if err := ts.App.Events.Upsert(t.Context(), models.Event{
		CalendarID: calID,
		UID:        "orphan-1",
		Summary:    "Orphan event",
		Actions:    []models.EventAction{{ID: "noop", Label: "Noop"}},
		StartsAt:   time.Now(),
		EndsAt:     time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Upsert event: %v", err)
	}
	events, err := ts.App.Events.ListInWindow(t.Context(), 7)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListInWindow: events=%+v err=%v", events, err)
	}

	resp, err := ts.Client.Post(ts.URL+"/kiosk/events/"+strconv.Itoa(events[0].ID)+"/actions/noop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, http.StatusInternalServerError, body)
	}
}

// TestKioskEventActionPostActionFails covers plugins.PostAction returning an
// error (plugin's /actions/{id} 500s), which must surface as a 502.
func TestKioskEventActionPostActionFails(t *testing.T) {
	ts := newTestServer(t)
	fixture := newActionPluginFixture(t, ts, http.StatusInternalServerError)

	if err := ts.App.Events.Upsert(t.Context(), models.Event{
		CalendarID: fixture.calendarID,
		UID:        "bill-4",
		Summary:    "Internet bill",
		Actions:    []models.EventAction{{ID: "mark-paid", Label: "Mark Paid"}},
		StartsAt:   time.Now(),
		EndsAt:     time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	events, err := ts.App.Events.ListInWindow(t.Context(), 7)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListInWindow: events=%+v err=%v", events, err)
	}

	resp, err := ts.Client.Post(ts.URL+"/kiosk/events/"+strconv.Itoa(events[0].ID)+"/actions/mark-paid", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, http.StatusBadGateway, body)
	}
}

// TestKioskEventActionDecryptTokenError covers the "decrypting token" 500
// branch: the owning plugin's stored token isn't valid ciphertext.
func TestKioskEventActionDecryptTokenError(t *testing.T) {
	ts := newTestServer(t)
	fixture := newActionPluginFixture(t, ts, http.StatusOK)
	if err := ts.App.Plugins.SetToken(t.Context(), fixture.pluginID, []byte("garbage")); err != nil {
		t.Fatalf("SetToken: %v", err)
	}

	if err := ts.App.Events.Upsert(t.Context(), models.Event{
		CalendarID: fixture.calendarID,
		UID:        "bill-5",
		Summary:    "Cable bill",
		Actions:    []models.EventAction{{ID: "mark-paid", Label: "Mark Paid"}},
		StartsAt:   time.Now(),
		EndsAt:     time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	events, err := ts.App.Events.ListInWindow(t.Context(), 7)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListInWindow: events=%+v err=%v", events, err)
	}

	resp, err := ts.Client.Post(ts.URL+"/kiosk/events/"+strconv.Itoa(events[0].ID)+"/actions/mark-paid", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, http.StatusInternalServerError, body)
	}
	if fixture.hits() != 0 {
		t.Fatalf("expected the plugin's /actions endpoint to never be called, got %d hits", fixture.hits())
	}
}

// --- TogglePlugin / PluginSettingsPage remaining branches ---

// TestTogglePluginNotFound covers TogglePlugin's ErrNotFound -> 404 branch.
func TestTogglePluginNotFound(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "toggle-404@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/plugins/no-such-plugin/toggle", csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// TestTogglePluginStoreErrorOnLookup covers TogglePlugin's generic-error 500
// branch at the initial GetByID lookup (a different branch from
// TestTogglePluginNotFound's ErrNotFound path).
func TestTogglePluginStoreErrorOnLookup(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "toggle-store-error@example.com", "s3cret-password")

	orig := ts.App.Plugins
	ts.App.Plugins = &models.PluginStore{DB: brokenDB(t)}
	defer func() { ts.App.Plugins = orig }()

	resp := ts.postForm(t, "/parent/plugins/whatever/toggle", csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestPluginSettingsPageNotFound covers PluginSettingsPage's ErrNotFound ->
// 404 branch.
func TestPluginSettingsPageNotFound(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "settings-404@example.com", "s3cret-password")

	resp, err := ts.Client.Get(ts.URL + "/parent/plugins/no-such-plugin/settings")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// TestPluginSettingsPageStoreErrorOnLookup covers PluginSettingsPage's
// generic-error 500 branch at the initial GetByID lookup.
func TestPluginSettingsPageStoreErrorOnLookup(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "settings-store-error@example.com", "s3cret-password")

	orig := ts.App.Plugins
	ts.App.Plugins = &models.PluginStore{DB: brokenDB(t)}
	defer func() { ts.App.Plugins = orig }()

	resp, err := ts.Client.Get(ts.URL + "/parent/plugins/whatever/settings")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestPluginSettingsPageDecryptTokenError covers PluginSettingsPage's
// "decrypting token" 500 branch.
func TestPluginSettingsPageDecryptTokenError(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "settings-decrypt-error@example.com", "s3cret-password")
	plugin := fakePluginServer(t, false, "", "", false)
	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "decrypt-error-plugin", BaseURL: plugin.URL, Enabled: true},
	})
	if err := ts.App.Plugins.SetToken(t.Context(), "decrypt-error-plugin", []byte("garbage")); err != nil {
		t.Fatalf("SetToken: %v", err)
	}

	resp, err := ts.Client.Get(ts.URL + "/parent/plugins/decrypt-error-plugin/settings")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, http.StatusInternalServerError, body)
	}
}

// TestKioskPluginViewFetchFailureShowsUnavailableMessage is a regression
// test for the kiosk previously rendering a totally blank fragment when a
// plugin crashed/became unreachable - the wall-mounted display gave no
// indication anything was wrong. Registers a plugin, then closes its server
// (simulating a crash) before hitting its kiosk nav view.
func TestKioskPluginViewFetchFailureShowsUnavailableMessage(t *testing.T) {
	ts := newTestServer(t)
	plugin := fakePluginServer(t, true, "Bills", "", false)
	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "crashed-plugin", Name: "Bill Tracker", BaseURL: plugin.URL, Enabled: true},
	})
	plugin.Close()

	resp, err := ts.Client.Get(ts.URL + "/kiosk/view/plugin/crashed-plugin")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Bill Tracker isn't available right now") {
		t.Fatalf("expected an informative unavailable message naming the plugin, got: %s", body)
	}
}

// TestPluginSettingsPageUnreachableRedirectsWithFriendlyModalError is a
// regression test: previously an unreachable plugin's settings page dumped
// the raw dial error ("dial tcp [::1]:8090: connectex: ...") onto a bare
// page. Now PluginSettingsPage redirects back to the dashboard with a
// simple message, which dashboard.html shows in a modal
// (#plugin-error-modal-overlay) instead of navigating away.
func TestPluginSettingsPageUnreachableRedirectsWithFriendlyModalError(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "settings-unreachable@example.com", "s3cret-password")
	plugin := fakePluginServer(t, false, "", "", false)
	ts.App.BootstrapPlugins(t.Context(), []config.PluginBootstrap{
		{ID: "unreachable-settings-plugin", Name: "Bill Tracker", BaseURL: plugin.URL, Enabled: true},
	})
	plugin.Close()

	resp, err := ts.Client.Get(ts.URL + "/parent/plugins/unreachable-settings-plugin/settings")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d (redirect back to dashboard); body: %s", resp.StatusCode, http.StatusSeeOther, body)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "plugin_settings_error=") {
		t.Fatalf("Location = %q, want it to carry plugin_settings_error", loc)
	}
	if strings.Contains(loc, "connectex") || strings.Contains(loc, "dial+tcp") {
		t.Fatalf("Location = %q, want a simple message, not a raw dial error", loc)
	}

	dashResp, err := ts.Client.Get(ts.URL + loc)
	if err != nil {
		t.Fatalf("GET dashboard: %v", err)
	}
	defer dashResp.Body.Close()
	dashBody, _ := io.ReadAll(dashResp.Body)
	if !strings.Contains(string(dashBody), "Bill Tracker isn&#39;t reachable right now") {
		t.Fatalf("expected the dashboard to show the friendly plugin error, got: %s", dashBody)
	}
	overlayIdx := strings.Index(string(dashBody), `id="plugin-error-modal-overlay"`)
	if overlayIdx == -1 {
		t.Fatalf("expected the plugin error modal markup, got: %s", dashBody)
	}
	overlayTagEnd := strings.Index(string(dashBody)[overlayIdx:], ">")
	overlayTag := string(dashBody)[overlayIdx : overlayIdx+overlayTagEnd]
	if strings.Contains(overlayTag, "hidden") {
		t.Fatalf("expected the plugin error modal to be visible (not hidden) when PluginSettingsError is set, got tag: %s", overlayTag)
	}
}
