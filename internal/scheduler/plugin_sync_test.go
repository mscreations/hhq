package scheduler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
	"github.com/mscreations/hhq/internal/util"
)

// newFakePluginEventsServer serves just GET /events - the only endpoint
// syncAllPlugins talks to - returning events for as long as respond is true.
func newFakePluginEventsServer(t *testing.T, respond *bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var events []map[string]any
		if *respond {
			events = []map[string]any{{
				"uid":       "electric-bill-1",
				"summary":   "Electric bill due",
				"all_day":   true,
				"starts_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"ends_at":   time.Now().Add(48 * time.Hour).Format(time.RFC3339),
			}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSyncAllPluginsUpsertsAndPrunesEvents exercises the real plugin sync
// loop (syncAllPlugins) against a real Postgres and a fake plugin server:
// a synthetic event is upserted into the plugin's dedicated calendar and
// shows up via the same ListInWindow read path the kiosk agenda/calendar
// panels use, and disappears again once the plugin stops reporting it -
// confirming PruneStale is scoped to the plugin's own calendar_id, the same
// isolation real CalDAV accounts rely on to not interfere with each other.
func TestSyncAllPluginsUpsertsAndPrunesEvents(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	respond := true
	srv := newFakePluginEventsServer(t, &respond)

	accounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}
	pluginStore := &models.PluginStore{DB: conn}

	accountID, err := accounts.Create(ctx, models.CalendarAccount{
		Name:             "Plugin: Bill Tracker",
		Provider:         models.ProviderPlugin,
		BootstrapManaged: true,
	})
	if err != nil {
		t.Fatalf("CalendarAccounts.Create: %v", err)
	}
	calendarID, err := calendars.UpsertDiscovered(ctx, accountID, "bill-tracker", "Bill Tracker", "#3B82F6")
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}
	encryptor, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	encryptedToken, err := encryptor.Encrypt("test-plugin-token")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if err := pluginStore.Create(ctx, models.Plugin{ID: "bill-tracker", Name: "Bill Tracker", BaseURL: srv.URL, Enabled: true, BootstrapManaged: true}); err != nil {
		t.Fatalf("Plugins.Create: %v", err)
	}
	if err := pluginStore.SetToken(ctx, "bill-tracker", encryptedToken); err != nil {
		t.Fatalf("Plugins.SetToken: %v", err)
	}
	if err := pluginStore.UpdateManifest(ctx, "bill-tracker", false, sql.NullString{}, sql.NullString{}, true); err != nil {
		t.Fatalf("UpdateManifest: %v", err)
	}
	if err := pluginStore.SetCalendarID(ctx, "bill-tracker", calendarID); err != nil {
		t.Fatalf("SetCalendarID: %v", err)
	}

	s := &Scheduler{
		Cfg:              &config.Config{CalendarWindowDays: 7},
		Events:           events,
		Plugins:          pluginStore,
		Calendars:        calendars,
		CalendarAccounts: accounts,
		Encryptor:        encryptor,
	}
	s.syncAllPlugins(ctx)

	inWindow, err := events.ListInWindow(ctx, 7)
	if err != nil {
		t.Fatalf("ListInWindow: %v", err)
	}
	if !containsUID(inWindow, "electric-bill-1") {
		t.Fatalf("expected the plugin's synthetic event to appear via ListInWindow, got %+v", inWindow)
	}

	// The plugin stops reporting the event - the next sync must prune it,
	// scoped only to this plugin's own calendar_id.
	respond = false
	s.syncAllPlugins(ctx)

	inWindowAfter, err := events.ListInWindow(ctx, 7)
	if err != nil {
		t.Fatalf("ListInWindow after prune: %v", err)
	}
	if containsUID(inWindowAfter, "electric-bill-1") {
		t.Fatal("expected the event to be pruned once the plugin stopped reporting it")
	}
}

func containsUID(list []models.Event, uid string) bool {
	for _, e := range list {
		if e.UID == uid {
			return true
		}
	}
	return false
}
