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
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
	"github.com/mscreations/hhq/internal/util"
	"github.com/mscreations/hhq/internal/weather"
)

// newFakeCalDAVServer returns a minimal fake CalDAV server exposing a single
// "Home" calendar via the standard principal/home-set/calendar-list discovery
// handshake. onReport handles each REPORT (event-query) request, so callers
// that need to observe/count sync ticks can hook in rather than always
// getting an empty response.
func newFakeCalDAVServer(t *testing.T, onReport func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	const principal = "/principals/user/"
	const homeSet = "/calendars/user/"
	const calPath = homeSet + "home/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/":
			testutil.WritePrincipalResponse(w, principal)
		case r.Method == "PROPFIND" && r.URL.Path == principal:
			testutil.WriteHomeSetResponse(w, principal, homeSet)
		case r.Method == "PROPFIND" && r.URL.Path == homeSet:
			testutil.WriteCalendarListResponse(w, homeSet, map[string]string{calPath: "Home"})
		case r.Method == "REPORT" && r.URL.Path == calPath:
			onReport(w)
		default:
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// pollUntil polls cond every interval until it returns true, failing the
// test if timeout elapses first. Used to wait for an async goroutine's
// database side effect to land, rather than guessing with a fixed sleep -
// a shared per-binary testcontainer under load from other tests in this
// package could take longer than any fixed guess.
func pollUntil(t *testing.T, timeout, interval time.Duration, cond func() bool, timeoutMsg func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(timeoutMsg())
		}
		time.Sleep(interval)
	}
}

// TestSyncAllAccountsSucceedsAgainstFakeCalDAVServer exercises the actual
// per-account sync loop (discovery + event fetch + MarkSynced) against a real
// Postgres and a fake CalDAV server, mirroring the end-to-end rigor CLAUDE.md
// established for internal/caldav.
func TestSyncAllAccountsSucceedsAgainstFakeCalDAVServer(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	srv := newFakeCalDAVServer(t, testutil.WriteEmptyCalendarQueryResponse)

	accounts := &models.CalendarAccountStore{DB: conn}
	calendars := &models.CalendarStore{DB: conn}
	events := &models.EventStore{DB: conn}

	encryptor, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	encrypted, err := encryptor.Encrypt("password")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	accountID, err := accounts.Create(ctx, models.CalendarAccount{
		Name:              "Test Account",
		Provider:          models.ProviderGeneric,
		CalDAVURL:         testSQLNullString(srv.URL),
		Username:          testSQLNullString("user"),
		EncryptedPassword: encrypted,
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}

	s := &Scheduler{
		Cfg:              &config.Config{CalendarWindowDays: 7},
		CalendarAccounts: accounts,
		Calendars:        calendars,
		Events:           events,
		Encryptor:        encryptor,
	}
	s.syncAllAccounts(ctx)

	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !account.LastSyncedAt.Valid || account.LastSyncError.Valid {
		t.Fatalf("expected a successful sync, got %+v", account)
	}
}

// TestSyncAllAccountsRecordsDecryptError covers the branch where a stored
// password can't be decrypted (e.g. the encryption key changed) - the account
// must be marked with the resulting error rather than the sync loop panicking
// or silently skipping it.
func TestSyncAllAccountsRecordsDecryptError(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	accounts := &models.CalendarAccountStore{DB: conn}
	encryptor, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	accountID, err := accounts.Create(ctx, models.CalendarAccount{
		Name:              "Broken Account",
		Provider:          models.ProviderGeneric,
		CalDAVURL:         testSQLNullString("https://example.invalid/"),
		Username:          testSQLNullString("user"),
		EncryptedPassword: []byte("not-valid-ciphertext"),
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}

	s := &Scheduler{
		Cfg:              &config.Config{CalendarWindowDays: 7},
		CalendarAccounts: accounts,
		Calendars:        &models.CalendarStore{DB: conn},
		Events:           &models.EventStore{DB: conn},
		Encryptor:        encryptor,
	}
	s.syncAllAccounts(ctx)

	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !account.LastSyncError.Valid {
		t.Fatal("expected a decrypt failure to be recorded as a sync error")
	}
}

// TestSyncAllAccountsRecordsUnimplementedGoogleProviderError covers the
// "not yet implemented" branch for the Google provider.
func TestSyncAllAccountsRecordsUnimplementedGoogleProviderError(t *testing.T) {
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

	accountID, err := accounts.Create(ctx, models.CalendarAccount{
		Name:              "Google Account",
		Provider:          models.ProviderGoogle,
		EncryptedPassword: encrypted,
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}

	s := &Scheduler{
		Cfg:              &config.Config{CalendarWindowDays: 7},
		CalendarAccounts: accounts,
		Calendars:        &models.CalendarStore{DB: conn},
		Events:           &models.EventStore{DB: conn},
		Encryptor:        encryptor,
	}
	s.syncAllAccounts(ctx)

	account, err := accounts.GetByID(ctx, accountID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !account.LastSyncError.Valid {
		t.Fatal("expected the unimplemented Google provider to be recorded as a sync error")
	}
}

// TestRunCalendarSyncTicksRepeatedly uses a very short sync interval to
// confirm runCalendarSync actually loops on its ticker (not just the
// once-on-startup call). It counts REPORT requests hitting a fake CalDAV
// server so a regression that broke the ticker loop (e.g. only the initial
// startup sync ever running) would be caught, rather than merely checking
// that runCalendarSync returns once its context expires.
func TestRunCalendarSyncTicksRepeatedly(t *testing.T) {
	conn := testutil.RequireDB(t)

	var reportCount atomic.Int32
	srv := newFakeCalDAVServer(t, func(w http.ResponseWriter) {
		reportCount.Add(1)
		testutil.WriteEmptyCalendarQueryResponse(w)
	})

	accounts := &models.CalendarAccountStore{DB: conn}
	encryptor, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	encrypted, err := encryptor.Encrypt("password")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := accounts.Create(t.Context(), models.CalendarAccount{
		Name:              "Test Account",
		Provider:          models.ProviderGeneric,
		CalDAVURL:         testSQLNullString(srv.URL),
		Username:          testSQLNullString("user"),
		EncryptedPassword: encrypted,
	}); err != nil {
		t.Fatalf("Create account: %v", err)
	}

	s := &Scheduler{
		Cfg:              &config.Config{CalendarSyncInterval: 20 * time.Millisecond, CalendarWindowDays: 7},
		CalendarAccounts: accounts,
		Calendars:        &models.CalendarStore{DB: conn},
		Events:           &models.EventStore{DB: conn},
		Encryptor:        encryptor,
	}

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	s.runCalendarSync(ctx)

	if got := reportCount.Load(); got < 2 {
		t.Fatalf("expected runCalendarSync's ticker to fire at least once beyond the initial startup sync (>= 2 REPORT requests), got %d", got)
	}
}

// TestRunDailyChoreGenerationGeneratesOnStartup confirms the immediate
// (pre-ticker) generate() call creates chore instances for today and the
// next two days without waiting an hour for the ticker to fire.
func TestRunDailyChoreGenerationGeneratesOnStartup(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx := t.Context()

	users := &models.UserStore{DB: conn}
	chores := &models.ChoreStore{DB: conn}
	defs := &models.ChoreDefinitionStore{DB: conn}
	instances := &models.ChoreInstanceStore{DB: conn}

	childID, err := users.CreateChild(ctx, "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	choreID, err := chores.Create(ctx, "Daily chore", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}
	if _, err := defs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	s := &Scheduler{ChoreInstances: instances}

	// generate() must complete against a still-live context (EnsureForDate
	// issues real queries), so wait for its actual DB write to land rather
	// than guessing with a fixed sleep - a shared per-binary testcontainer
	// under load from other tests in this package could take longer than
	// any fixed guess, and canceling mid-query would abort the write.
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		s.runDailyChoreGeneration(runCtx)
		close(done)
	}()

	var today []models.ChoreInstance
	pollUntil(t, 2*time.Second, 5*time.Millisecond, func() bool {
		var err error
		today, err = instances.ListForDate(ctx, time.Now())
		if err != nil {
			t.Fatalf("ListForDate: %v", err)
		}
		return len(today) == 1
	}, func() string {
		return fmt.Sprintf("timed out waiting for the startup generate() call to create today's chore instance, got %+v", today)
	})

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runDailyChoreGeneration did not return promptly after context cancellation")
	}
}

// TestRunSessionCleanupTicksAndCleansUpExpiredSessions uses a Scheduler with
// a real Postgres-backed SessionStore and drives runSessionCleanup directly
// with a short-lived context; since the ticker interval itself is a fixed
// 6h, this test only exercises the immediate ctx.Done() shutdown path, but
// does so against the real store/limiter to catch any wiring mistakes (e.g.
// a nil pointer) that unit tests of DeleteExpired alone wouldn't catch.
func TestRunSessionCleanupReturnsOnContextCancellation(t *testing.T) {
	conn := testutil.RequireDB(t)

	s := &Scheduler{
		Sessions:     &models.SessionStore{DB: conn},
		LoginLimiter: auth.NewLoginLimiter(10, 15*time.Minute),
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan struct{})
	go func() {
		s.runSessionCleanup(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runSessionCleanup did not return promptly after context cancellation")
	}
}

// TestRunWeeklyReportReturnsOnContextCancellation mirrors
// TestRunSessionCleanupReturnsOnContextCancellation for the weekly report
// loop - the actual send logic is separately covered end-to-end by
// TestSendWeeklyReportEmailsAllParentsWithGeneratedPDF in weekly_report_test.go.
func TestRunWeeklyReportReturnsOnContextCancellation(t *testing.T) {
	conn := testutil.RequireDB(t)

	s := &Scheduler{
		Cfg:            &config.Config{WeeklyReportWeekday: time.Sunday, WeeklyReportHour: 20},
		ChoreInstances: &models.ChoreInstanceStore{DB: conn},
		Users:          &models.UserStore{DB: conn},
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan struct{})
	go func() {
		s.runWeeklyReport(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWeeklyReport did not return promptly after context cancellation")
	}
}

// TestRunLaunchesAllFourJobsAndStopsOnCancel exercises Scheduler.Run itself
// (the production entry point called once from cmd/server/main.go) rather
// than only its constituent job functions individually. It asserts on the
// two jobs with synchronous startup side effects (calendar sync's initial
// sync, daily chore generation's initial generate()) to confirm Run actually
// dispatches those goroutines, rather than a regression (e.g. a deleted `go`
// statement) passing silently. Session cleanup and the weekly report have no
// startup-time side effect to observe (their tickers only fire hours later)
// and are covered directly by TestRunSessionCleanupReturnsOnContextCancellation
// and TestRunWeeklyReportReturnsOnContextCancellation instead.
//
// The calendar-sync side effect is observed via a deliberately-undecryptable
// account (same technique as TestSyncAllAccountsRecordsDecryptError) rather
// than a real fake CalDAV server - this test only needs proof that
// runCalendarSync was dispatched and reached MarkSynced, not that the CalDAV
// wire protocol itself works (already covered by
// TestSyncAllAccountsSucceedsAgainstFakeCalDAVServer and
// TestRunCalendarSyncTicksRepeatedly), so it shouldn't also break on an
// unrelated CalDAV wire-format change.
func TestRunLaunchesAllFourJobsAndStopsOnCancel(t *testing.T) {
	conn := testutil.RequireDB(t)

	accounts := &models.CalendarAccountStore{DB: conn}
	encryptor, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	accountID, err := accounts.Create(t.Context(), models.CalendarAccount{
		Name:              "Test Account",
		Provider:          models.ProviderGeneric,
		CalDAVURL:         testSQLNullString("https://example.invalid/"),
		Username:          testSQLNullString("user"),
		EncryptedPassword: []byte("not-valid-ciphertext"),
	})
	if err != nil {
		t.Fatalf("Create account: %v", err)
	}

	users := &models.UserStore{DB: conn}
	chores := &models.ChoreStore{DB: conn}
	defs := &models.ChoreDefinitionStore{DB: conn}
	instances := &models.ChoreInstanceStore{DB: conn}
	childID, err := users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	choreID, err := chores.Create(t.Context(), "Daily chore", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}
	if _, err := defs.CreateRecurring(t.Context(), childID, choreID, 5, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	s := &Scheduler{
		Cfg:              &config.Config{CalendarSyncInterval: time.Hour, CalendarWindowDays: 7, WeeklyReportWeekday: time.Sunday, WeeklyReportHour: 20, WeatherRefreshInterval: time.Hour, PluginSyncInterval: time.Hour},
		CalendarAccounts: accounts,
		Calendars:        &models.CalendarStore{DB: conn},
		Events:           &models.EventStore{DB: conn},
		Encryptor:        encryptor,
		ChoreInstances:   instances,
		Users:            users,
		Sessions:         &models.SessionStore{DB: conn},
		Settings:         &models.SettingsStore{DB: conn},
		LoginLimiter:     auth.NewLoginLimiter(10, 15*time.Minute),
		Weather:          &weather.Cache{},
		Plugins:          &models.PluginStore{DB: conn},
	}

	ctx, cancel := context.WithCancel(t.Context())
	s.Run(ctx)
	// Run launches its jobs in goroutines and returns immediately; poll for
	// the two jobs' startup side effects instead of guessing with a fixed
	// sleep before canceling.
	var account *models.CalendarAccount
	var today []models.ChoreInstance
	pollUntil(t, 2*time.Second, 5*time.Millisecond, func() bool {
		var err error
		account, err = accounts.GetByID(t.Context(), accountID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		today, err = instances.ListForDate(t.Context(), time.Now())
		if err != nil {
			t.Fatalf("ListForDate: %v", err)
		}
		return account.LastSyncError.Valid && len(today) == 1
	}, func() string {
		return fmt.Sprintf("timed out waiting for Run's calendar-sync and chore-generation jobs to complete their startup work (account sync error recorded=%v, chore instances=%d)", account.LastSyncError.Valid, len(today))
	})
	cancel()
}

func testSQLNullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: true}
}
