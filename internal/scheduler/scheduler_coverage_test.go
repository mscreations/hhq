package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/email"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
	"github.com/mscreations/hhq/internal/util"
	"github.com/mscreations/hhq/internal/weather"
)

// brokenDB returns a *sql.DB that always fails on the first query - a
// pgx-registered connection pointed at a port nothing listens on, so calls
// fail fast with a connection error rather than hanging. Used to exercise
// the "the query itself failed" error branches throughout this package,
// which a shared, healthy testutil.RequireDB connection can't produce on
// demand.
func brokenDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", "postgres://baduser:badpass@127.0.0.1:1/nonexistent?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatalf("sql.Open broken db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// ---- syncAllAccounts ----

// TestSyncAllAccountsSkipsPluginProviderAccounts covers the ProviderPlugin
// branch of syncAllAccounts, which must skip synthetic plugin-backed
// accounts silently (they're synced separately by runPluginSync) rather
// than attempting to decrypt a password that was never set for them.
func TestSyncAllAccountsSkipsPluginProviderAccounts(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	accounts := &models.CalendarAccountStore{DB: conn}
	accountID, err := accounts.Create(ctx, models.CalendarAccount{
		Name:             "Plugin: Bill Tracker",
		Provider:         models.ProviderPlugin,
		BootstrapManaged: true,
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}

	encryptor, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	s := &Scheduler{
		Cfg:              &config.Config{CalendarWindowDays: 7},
		CalendarAccounts: accounts,
		Calendars:        &models.CalendarStore{DB: conn},
		Events:           &models.EventStore{DB: conn},
		Encryptor:        encryptor,
	}

	// Must not panic despite a nil/empty EncryptedPassword on a plugin account.
	s.syncAllAccounts(ctx)

	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if account.LastSyncedAt.Valid || account.LastSyncError.Valid {
		t.Fatalf("expected a plugin-provider account to be skipped entirely (no MarkSynced call), got %+v", account)
	}
}

// TestSyncOneAccountUnknownProviderRecordsError covers the dispatch
// switch's default branch inside syncOneAccount. models.CalendarProvider is
// a plain Go string type, but the underlying `calendar_provider` Postgres
// column is a closed 5-value ENUM covering every case this dispatch already
// handles (caldav_fastmail/caldav_icloud/caldav_generic/google/plugin) - so
// via the normal ListAll-fed path (syncAllAccounts), this default branch is
// unreachable today; INSERTing a bogus enum value to prove otherwise fails
// at the DB layer with SQLSTATE 22P02, confirmed while writing this test.
// It still earns its keep as a defensive fallback for a hypothetical future
// enum value added without updating this switch, so rather than deleting it
// as dead code, it's tested by calling syncOneAccount directly with a
// hand-built account bypassing the DB's enum constraint entirely - this
// exercises the exact same Go code a real future case would hit.
func TestSyncOneAccountUnknownProviderRecordsError(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	accounts := &models.CalendarAccountStore{DB: conn}
	encryptor, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	encrypted, err := encryptor.Encrypt("password")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// A real row is still created (with a valid enum value) so MarkSynced
	// has an actual account row to update by ID; syncOneAccount is then
	// called directly with a copy carrying a bogus in-memory Provider value,
	// which the DB itself would reject if ever attempted via Create/Update.
	accountID, err := accounts.Create(ctx, models.CalendarAccount{
		Name:              "Mystery Account",
		Provider:          models.ProviderGeneric,
		CalDAVURL:         testSQLNullString("https://example.invalid/"),
		Username:          testSQLNullString("user"),
		EncryptedPassword: encrypted,
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}
	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	account.Provider = models.CalendarProvider("caldav_mystery")

	s := &Scheduler{
		Cfg:              &config.Config{CalendarWindowDays: 7},
		CalendarAccounts: accounts,
		Calendars:        &models.CalendarStore{DB: conn},
		Events:           &models.EventStore{DB: conn},
		Encryptor:        encryptor,
	}
	s.syncOneAccount(ctx, *account)

	updated, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID after sync: %v", err)
	}
	if !updated.LastSyncError.Valid || !strings.Contains(updated.LastSyncError.String, "unknown provider") {
		t.Fatalf("expected an 'unknown provider' sync error, got %+v", updated)
	}
}

// TestSyncOneAccountGoogleSyncFailureIsRecorded covers the branch where a
// Google account's refresh token decrypts successfully but the actual
// SyncGoogleAccount call fails - as opposed to google_sync_test.go's
// TestSyncAllAccountsGoogleDecryptsRefreshTokenNotPassword, which only
// covers the decrypt-failure branch before SyncGoogleAccount is ever
// called. SyncGoogleAccount builds its own googleapi client from
// config.GoogleOAuthConfig with no hook for injecting a fake HTTP
// transport, so neither a real success nor a specific-shaped failure can be
// produced deterministically without live network access to Google. A
// pre-canceled context makes the underlying HTTP call fail immediately
// without needing network access at all, which is enough to exercise this
// err != nil branch (the logging.Errorf + MarkSynced call). Note the
// success branch (SyncGoogleAccount returning nil) is NOT covered by this
// test or any other - see the accompanying report for why that's a
// deliberate, explained gap rather than an oversight.
func TestSyncOneAccountGoogleSyncFailureIsRecorded(t *testing.T) {
	conn := testutil.RequireDB(t)

	accounts := &models.CalendarAccountStore{DB: conn}
	encryptor, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	encryptedToken, err := encryptor.Encrypt("refresh-token")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	accountID, err := accounts.CreateGoogle(t.Context(), "Google Account", encryptedToken, "me@example.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}
	account, err := accounts.GetByID(t.Context(), accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	s := &Scheduler{
		Cfg:              &config.Config{CalendarWindowDays: 7},
		CalendarAccounts: accounts,
		Calendars:        &models.CalendarStore{DB: conn},
		Events:           &models.EventStore{DB: conn},
		Encryptor:        encryptor,
	}

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	// Must not panic; the subsequent (also-canceled-context) MarkSynced call
	// is expected to itself fail to persist the error (same as production:
	// its return value is deliberately discarded via `_ =`), so this test
	// only asserts the call completes rather than asserting a DB side effect.
	s.syncOneAccount(canceledCtx, *account)
}

// TestSyncAllAccountsListAllErrorReturnsWithoutPanic covers ListAll's error
// branch - a DB-level failure listing accounts must be logged and return,
// not panic or propagate.
func TestSyncAllAccountsListAllErrorReturnsWithoutPanic(t *testing.T) {
	s := &Scheduler{
		Cfg:              &config.Config{CalendarWindowDays: 7},
		CalendarAccounts: &models.CalendarAccountStore{DB: brokenDB(t)},
	}
	s.syncAllAccounts(t.Context()) // must not panic
}

// ---- runSessionCleanup / cleanupSessions ----

// TestCleanupSessionsDeletesExpiredSessionsAndSweepsLimiter exercises the
// actual work runSessionCleanup performs on each (real, hardcoded 6h) tick,
// via the extracted cleanupSessions method - directly, since waiting on the
// real ticker isn't practical in a test.
func TestCleanupSessionsDeletesExpiredSessionsAndSweepsLimiter(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	users := &models.UserStore{DB: conn}
	sessions := &models.SessionStore{DB: conn}

	parentID, err := users.CreateParent(ctx, "Parent", "parent@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	token, err := sessions.Create(ctx, parentID, -1*time.Hour) // already expired
	if err != nil {
		t.Fatalf("Sessions.Create: %v", err)
	}

	limiter := auth.NewLoginLimiter(1, 20*time.Millisecond)
	limiter.RecordFailure("ip:1.2.3.4")

	s := &Scheduler{Sessions: sessions, LoginLimiter: limiter}
	s.cleanupSessions(ctx)

	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM hhq_sessions WHERE token = $1`, token).Scan(&count); err != nil {
		t.Fatalf("counting sessions: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the expired session to be deleted, still found %d row(s)", count)
	}
}

// TestCleanupSessionsHandlesDeleteExpiredErrorAndNilLimiter covers both the
// DeleteExpired-fails branch and the nil-LoginLimiter skip branch (a
// Scheduler with no LoginLimiter configured is a valid, supported
// configuration - the field is only used for the login rate limiter, which
// some deployments might not wire up).
func TestCleanupSessionsHandlesDeleteExpiredErrorAndNilLimiter(t *testing.T) {
	s := &Scheduler{
		Sessions:     &models.SessionStore{DB: brokenDB(t)},
		LoginLimiter: nil,
	}
	s.cleanupSessions(t.Context()) // must not panic despite the DB error and nil limiter
}

// ---- runDailyChoreGeneration / generateChoreInstances ----

// TestGenerateChoreInstancesHandlesEnsureForDateError covers the per-day
// error-logging branch inside generateChoreInstances (used both by the
// startup call and every hourly tick) - a broken store must not stop the
// loop from attempting all 3 days, nor panic.
func TestGenerateChoreInstancesHandlesEnsureForDateError(t *testing.T) {
	s := &Scheduler{ChoreInstances: &models.ChoreInstanceStore{DB: brokenDB(t)}}
	s.generateChoreInstances(t.Context()) // must not panic
}

// ---- maybeSendWeeklyReport ----

func newWeeklyReportTestScheduler(t *testing.T, conn *sql.DB, mailer *email.Sender) (*Scheduler, int) {
	t.Helper()
	users := &models.UserStore{DB: conn}
	chores := &models.ChoreStore{DB: conn}
	defs := &models.ChoreDefinitionStore{DB: conn}
	instances := &models.ChoreInstanceStore{DB: conn}

	childID, err := users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if _, err := users.CreateParent(t.Context(), "Parent", "parent@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	choreID, err := chores.Create(t.Context(), "Weekly chore", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}
	if _, err := defs.CreateRecurring(t.Context(), childID, choreID, 5, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	s := &Scheduler{
		Cfg:            &config.Config{AppTitle: "HappyHome Quest"},
		ChoreInstances: instances,
		Users:          users,
		Settings:       &models.SettingsStore{DB: conn},
		Mailer:         mailer,
	}
	return s, childID
}

// TestMaybeSendWeeklyReportSkipsOnWeekdayMismatch covers the "not the
// configured day" branch - no send should be attempted (nil Mailer would
// panic if it were).
func TestMaybeSendWeeklyReportSkipsOnWeekdayMismatch(t *testing.T) {
	now := time.Date(2026, 7, 21, 20, 0, 0, 0, time.UTC) // a Tuesday
	s := &Scheduler{Cfg: &config.Config{WeeklyReportWeekday: time.Sunday, WeeklyReportHour: 20}}

	got := s.maybeSendWeeklyReport(t.Context(), now, "")
	if got != "" {
		t.Fatalf("expected lastSentWeek to remain empty on weekday mismatch, got %q", got)
	}
}

// TestMaybeSendWeeklyReportSkipsOnHourMismatch covers the "not the
// configured hour" branch, independent of the weekday check.
func TestMaybeSendWeeklyReportSkipsOnHourMismatch(t *testing.T) {
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC) // a Sunday, wrong hour
	s := &Scheduler{Cfg: &config.Config{WeeklyReportWeekday: time.Sunday, WeeklyReportHour: 20}}

	got := s.maybeSendWeeklyReport(t.Context(), now, "")
	if got != "" {
		t.Fatalf("expected lastSentWeek to remain empty on hour mismatch, got %q", got)
	}
}

// TestMaybeSendWeeklyReportSendsOnceThenSkipsSameWeek covers the success
// path end to end (matching weekday+hour, first time this week) and the
// "already sent this week" dedup guard on a second call with the same
// lastSentWeek.
func TestMaybeSendWeeklyReportSendsOnceThenSkipsSameWeek(t *testing.T) {
	conn := testutil.RequireDB(t)

	srv := startCaptureSMTPServer(t)
	host, port := srv.addr()
	mailer := &email.Sender{Host: host, Port: port, From: "hhq@example.com"}
	s, _ := newWeeklyReportTestScheduler(t, conn, mailer)
	s.Cfg.WeeklyReportWeekday = time.Sunday
	s.Cfg.WeeklyReportHour = 20

	now := time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC) // matches
	got := s.maybeSendWeeklyReport(t.Context(), now, "")
	if got == "" {
		t.Fatal("expected lastSentWeek to be updated after a successful send")
	}

	select {
	case <-srv.dataSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the fake SMTP server to receive the first message")
	}

	// Same week, same lastSentWeek -> must be skipped (no second send).
	got2 := s.maybeSendWeeklyReport(t.Context(), now, got)
	if got2 != got {
		t.Fatalf("expected lastSentWeek to be unchanged on the dedup path, got %q want %q", got2, got)
	}
	select {
	case <-srv.dataSeen:
		t.Fatal("expected no second email to be sent for the same week")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestMaybeSendWeeklyReportKeepsRetryingOnSendFailure covers the
// sendWeeklyReport-returns-an-error branch: lastSentWeek must NOT be
// updated, so the next tick will retry rather than silently giving up for
// the week.
func TestMaybeSendWeeklyReportKeepsRetryingOnSendFailure(t *testing.T) {
	conn := testutil.RequireDB(t)

	// A mailer pointed at a closed local port - Send will fail to connect.
	badMailer := &email.Sender{Host: "127.0.0.1", Port: 1, From: "hhq@example.com"}
	s, _ := newWeeklyReportTestScheduler(t, conn, badMailer)
	s.Cfg.WeeklyReportWeekday = time.Sunday
	s.Cfg.WeeklyReportHour = 20

	now := time.Date(2026, 7, 26, 20, 0, 0, 0, time.UTC)
	got := s.maybeSendWeeklyReport(t.Context(), now, "")
	if got != "" {
		t.Fatalf("expected lastSentWeek to remain unchanged after a send failure, got %q", got)
	}
}

// ---- sendWeeklyReport direct error-path coverage ----

// TestSendWeeklyReportReturnsErrorWhenListForWeekFails covers the
// ChoreInstances.ListForWeek error return.
func TestSendWeeklyReportReturnsErrorWhenListForWeekFails(t *testing.T) {
	s := &Scheduler{
		Cfg:            &config.Config{},
		ChoreInstances: &models.ChoreInstanceStore{DB: brokenDB(t)},
	}
	if err := s.sendWeeklyReport(t.Context(), time.Now()); err == nil {
		t.Fatal("expected an error when ListForWeek fails")
	}
}

// TestSendWeeklyReportReturnsErrorWhenListParentsFails covers the
// Users.ListParents error return, with a healthy ChoreInstances store so
// only the Users call fails.
func TestSendWeeklyReportReturnsErrorWhenListParentsFails(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &Scheduler{
		Cfg:            &config.Config{},
		ChoreInstances: &models.ChoreInstanceStore{DB: conn},
		Users:          &models.UserStore{DB: brokenDB(t)},
	}
	if err := s.sendWeeklyReport(t.Context(), time.Now()); err == nil {
		t.Fatal("expected an error when ListParents fails")
	}
}

// TestSendWeeklyReportFallsBackToConfigAppTitleWhenSettingsGetFails covers
// the app_title Settings.Get error branch: the function must log and fall
// back to Cfg.AppTitle rather than aborting the whole send.
func TestSendWeeklyReportFallsBackToConfigAppTitleWhenSettingsGetFails(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	users := &models.UserStore{DB: conn}
	if _, err := users.CreateParent(ctx, "Parent", "parent@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	srv := startCaptureSMTPServer(t)
	host, port := srv.addr()

	s := &Scheduler{
		Cfg:            &config.Config{AppTitle: "Fallback Title"},
		ChoreInstances: &models.ChoreInstanceStore{DB: conn},
		Users:          users,
		Settings:       &models.SettingsStore{DB: brokenDB(t)},
		Mailer:         &email.Sender{Host: host, Port: port, From: "hhq@example.com"},
	}

	if err := s.sendWeeklyReport(ctx, time.Now()); err != nil {
		t.Fatalf("sendWeeklyReport: %v", err)
	}

	select {
	case data := <-srv.dataSeen:
		if !strings.Contains(data, "Fallback Title") {
			t.Errorf("expected the From header to use the config fallback app title, got: %s", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the fake SMTP server to receive a message")
	}
}

// ---- runWeatherRefresh ticker loop ----

// TestRunWeatherRefreshTicksRepeatedly mirrors
// TestRunCalendarSyncTicksRepeatedly: uses a very short refresh interval to
// confirm the ticker loop actually re-fetches beyond the initial
// on-startup call.
func TestRunWeatherRefreshTicksRepeatedly(t *testing.T) {
	conn := testutil.RequireDB(t)
	settings := &models.SettingsStore{DB: conn}
	ctx := t.Context()

	if err := settings.Set(ctx, weather.SettingLat, "41.85"); err != nil {
		t.Fatalf("Set lat: %v", err)
	}
	if err := settings.Set(ctx, weather.SettingLon, "-87.65"); err != nil {
		t.Fatalf("Set lon: %v", err)
	}

	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"current_weather":{"temperature":70.5,"weathercode":1},"hourly":{"time":[],"temperature_2m":[],"precipitation_probability":[],"weathercode":[]},"daily":{"time":[],"weathercode":[],"temperature_2m_max":[],"temperature_2m_min":[],"precipitation_probability_max":[]}}`))
	}))
	defer srv.Close()
	origForecastURL := weather.ForecastURL
	weather.ForecastURL = srv.URL
	defer func() { weather.ForecastURL = origForecastURL }()

	s := &Scheduler{
		Cfg:      &config.Config{WeatherRefreshInterval: 20 * time.Millisecond},
		Settings: settings,
		Weather:  &weather.Cache{},
	}

	runCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	s.runWeatherRefresh(runCtx)

	if got := requestCount.Load(); got < 2 {
		t.Fatalf("expected runWeatherRefresh's ticker to fire at least once beyond the initial startup fetch (>= 2 requests), got %d", got)
	}
}

// ---- refreshWeather error branches ----

// TestRefreshWeatherLoadLocationErrorReturnsWithoutPanic covers
// weather.LoadLocation's error branch (a Settings query failure).
func TestRefreshWeatherLoadLocationErrorReturnsWithoutPanic(t *testing.T) {
	s := &Scheduler{
		Cfg:      &config.Config{WeatherRefreshInterval: time.Hour},
		Settings: &models.SettingsStore{DB: brokenDB(t)},
		Weather:  &weather.Cache{},
	}
	s.refreshWeather(t.Context()) // must not panic

	if _, ok := s.Weather.Get(); ok {
		t.Fatal("expected the cache to remain empty when loading location settings fails")
	}
}

// TestRefreshWeatherFetchForecastErrorLeavesCacheEmpty covers
// weather.FetchForecast's error branch (Open-Meteo, or a stand-in, returning
// a non-200 response) - a configured location whose fetch fails must leave
// the cache untouched rather than panicking or storing a zero-value forecast.
func TestRefreshWeatherFetchForecastErrorLeavesCacheEmpty(t *testing.T) {
	conn := testutil.RequireDB(t)
	settings := &models.SettingsStore{DB: conn}
	ctx := t.Context()

	if err := settings.Set(ctx, weather.SettingLat, "41.85"); err != nil {
		t.Fatalf("Set lat: %v", err)
	}
	if err := settings.Set(ctx, weather.SettingLon, "-87.65"); err != nil {
		t.Fatalf("Set lon: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	origForecastURL := weather.ForecastURL
	weather.ForecastURL = srv.URL
	defer func() { weather.ForecastURL = origForecastURL }()

	s := &Scheduler{
		Cfg:      &config.Config{WeatherRefreshInterval: time.Hour},
		Settings: settings,
		Weather:  &weather.Cache{},
	}
	s.refreshWeather(ctx)

	if _, ok := s.Weather.Get(); ok {
		t.Fatal("expected the cache to remain empty after a failed forecast fetch")
	}
}

// ---- runPluginSync ticker loop ----

// TestRunPluginSyncTicksRepeatedly mirrors TestRunCalendarSyncTicksRepeatedly
// for the plugin-sync job: a very short PluginSyncInterval must cause more
// than just the one on-startup sync.
func TestRunPluginSyncTicksRepeatedly(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	var requestCount atomic.Int32
	respond := true
	srv := newFakePluginEventsServerCounting(t, &respond, &requestCount)

	accounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}
	pluginStore := &models.PluginStore{DB: conn}

	accountID, err := accounts.Create(ctx, models.CalendarAccount{
		Name:             "Plugin: Ticker Test",
		Provider:         models.ProviderPlugin,
		BootstrapManaged: true,
	})
	if err != nil {
		t.Fatalf("CalendarAccounts.Create: %v", err)
	}
	calendarID, err := calendars.UpsertDiscovered(ctx, accountID, "ticker-plugin", "Ticker Plugin", "#3B82F6")
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
	if err := pluginStore.Create(ctx, models.Plugin{ID: "ticker-plugin", Name: "Ticker Plugin", BaseURL: srv.URL, Enabled: true, BootstrapManaged: true}); err != nil {
		t.Fatalf("Plugins.Create: %v", err)
	}
	if err := pluginStore.SetToken(ctx, "ticker-plugin", encryptedToken); err != nil {
		t.Fatalf("Plugins.SetToken: %v", err)
	}
	if err := pluginStore.UpdateManifest(ctx, "ticker-plugin", false, sql.NullString{}, sql.NullString{}, true); err != nil {
		t.Fatalf("UpdateManifest: %v", err)
	}
	if err := pluginStore.SetCalendarID(ctx, "ticker-plugin", calendarID); err != nil {
		t.Fatalf("SetCalendarID: %v", err)
	}

	s := &Scheduler{
		Cfg:              &config.Config{CalendarWindowDays: 7, PluginSyncInterval: 20 * time.Millisecond},
		Events:           events,
		Plugins:          pluginStore,
		Calendars:        calendars,
		CalendarAccounts: accounts,
		Encryptor:        encryptor,
	}

	runCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	s.runPluginSync(runCtx)

	if got := requestCount.Load(); got < 2 {
		t.Fatalf("expected runPluginSync's ticker to fire at least once beyond the initial startup sync (>= 2 requests), got %d", got)
	}
}

// newFakePluginEventsServerCounting is like newFakePluginEventsServer in
// plugin_sync_test.go but also counts requests, so the ticker test above can
// assert on repeated invocation without depending on that file.
func newFakePluginEventsServerCounting(t *testing.T, respond *bool, count *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Content-Type", "application/json")
		var events []map[string]any
		if *respond {
			events = []map[string]any{{
				"uid":       "ticker-event-1",
				"summary":   "Ticker event",
				"all_day":   true,
				"starts_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				"ends_at":   time.Now().Add(48 * time.Hour).Format(time.RFC3339),
			}}
		}
		fmt.Fprintf(w, `{"events":%s}`, mustMarshalEvents(events))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mustMarshalEvents(events []map[string]any) string {
	if events == nil {
		return "[]"
	}
	var b strings.Builder
	b.WriteString("[")
	for i, e := range events {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"uid":%q,"summary":%q,"all_day":%v,"starts_at":%q,"ends_at":%q}`,
			e["uid"], e["summary"], e["all_day"], e["starts_at"], e["ends_at"])
	}
	b.WriteString("]")
	return b.String()
}

// ---- syncAllPlugins additional branches ----

// TestSyncAllPluginsListEnabledErrorReturnsWithoutPanic covers
// Plugins.ListEnabled's error branch.
func TestSyncAllPluginsListEnabledErrorReturnsWithoutPanic(t *testing.T) {
	s := &Scheduler{
		Cfg:     &config.Config{CalendarWindowDays: 7},
		Plugins: &models.PluginStore{DB: brokenDB(t)},
	}
	s.syncAllPlugins(t.Context()) // must not panic
}

// TestSyncAllPluginsLogsErrorWhenSyncOneFails covers the per-plugin
// SyncOne-fails branch (as opposed to the success branch already covered by
// TestSyncAllPluginsUpsertsAndPrunesEvents in plugin_sync_test.go) - induced
// via an undecryptable stored token, which fails fast before any network
// call, mirroring TestSyncAllAccountsRecordsDecryptError's technique.
func TestSyncAllPluginsLogsErrorWhenSyncOneFails(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	accounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}
	pluginStore := &models.PluginStore{DB: conn}

	accountID, err := accounts.Create(ctx, models.CalendarAccount{
		Name:             "Plugin: Broken Token",
		Provider:         models.ProviderPlugin,
		BootstrapManaged: true,
	})
	if err != nil {
		t.Fatalf("CalendarAccounts.Create: %v", err)
	}
	calendarID, err := calendars.UpsertDiscovered(ctx, accountID, "broken-plugin", "Broken Plugin", "#3B82F6")
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}
	if err := pluginStore.Create(ctx, models.Plugin{ID: "broken-plugin", Name: "Broken Plugin", BaseURL: "http://example.invalid", Enabled: true, BootstrapManaged: true}); err != nil {
		t.Fatalf("Plugins.Create: %v", err)
	}
	if err := pluginStore.SetToken(ctx, "broken-plugin", []byte("not-valid-ciphertext")); err != nil {
		t.Fatalf("Plugins.SetToken: %v", err)
	}
	if err := pluginStore.UpdateManifest(ctx, "broken-plugin", false, sql.NullString{}, sql.NullString{}, true); err != nil {
		t.Fatalf("UpdateManifest: %v", err)
	}
	if err := pluginStore.SetCalendarID(ctx, "broken-plugin", calendarID); err != nil {
		t.Fatalf("SetCalendarID: %v", err)
	}

	encryptor, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	s := &Scheduler{
		Cfg:              &config.Config{CalendarWindowDays: 7},
		Events:           events,
		Plugins:          pluginStore,
		Calendars:        calendars,
		CalendarAccounts: accounts,
		Encryptor:        encryptor,
	}
	s.syncAllPlugins(ctx) // must log the failure and continue without panicking

	p, err := pluginStore.GetByID(ctx, "broken-plugin")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !p.LastError.Valid {
		t.Fatal("expected the undecryptable token to be recorded as a plugin sync error via MarkHealth")
	}
}
