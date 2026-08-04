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
	"bufio"
	"database/sql"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/email"
	"github.com/mscreations/hhq/internal/handlers"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
	"github.com/mscreations/hhq/internal/util"
	"github.com/mscreations/hhq/internal/weather"
	webassets "github.com/mscreations/hhq/web"
)

// testFakeSMTP is a minimal SMTP server (same shape as internal/email's and
// internal/scheduler's fakes) used so handler tests exercising the parent
// notification/invite email paths don't need a real mail server.
type testFakeSMTP struct {
	listener net.Listener
	received chan string
}

func startTestFakeSMTP(t *testing.T) *testFakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &testFakeSMTP{listener: ln, received: make(chan string, 10)}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.handle(conn)
		}
	}()
	return srv
}

func (s *testFakeSMTP) addr() (string, int) {
	return "127.0.0.1", s.listener.Addr().(*net.TCPAddr).Port
}

func (s *testFakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	fmt.Fprint(conn, "220 fake ESMTP\r\n")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		upper := strings.ToUpper(strings.TrimRight(line, "\r\n"))
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			fmt.Fprint(conn, "250-fake\r\n250 AUTH PLAIN\r\n")
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			fmt.Fprint(conn, "235 OK\r\n")
		case strings.HasPrefix(upper, "MAIL FROM:"), strings.HasPrefix(upper, "RCPT TO:"):
			fmt.Fprint(conn, "250 OK\r\n")
		case upper == "DATA":
			fmt.Fprint(conn, "354 go ahead\r\n")
			var body strings.Builder
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				body.WriteString(dl)
			}
			s.received <- body.String()
			fmt.Fprint(conn, "250 OK: queued\r\n")
		case upper == "QUIT":
			fmt.Fprint(conn, "221 Bye\r\n")
			return
		default:
			fmt.Fprint(conn, "250 OK\r\n")
		}
	}
}

// testServer builds a real *handlers.App backed by a real, migrated Postgres
// (via testutil) and real embedded templates, wires it through the actual
// buildRouter used in production, and serves it over httptest.NewServer so
// tests drive it exactly like a browser would (real HTTP, real cookies) -
// following the "real Postgres + real binary" verification approach CLAUDE.md
// used to validate the CalDAV and htmx work.
type testServer struct {
	*httptest.Server
	App    *handlers.App
	Client *http.Client
	SMTP   *testFakeSMTP
}

// newApp builds a *handlers.App wired to conn, with the common set of
// stores/signers/config shared by every test server variant in this file.
// Callers set any variant-specific fields (e.g. Mailer) afterward - this
// exists so the next new App field only needs to be added in one place,
// rather than kept in sync by hand across multiple constructors (which
// previously let newBrokenDBTestServer drift out of sync with newTestServer
// and silently omit fields it needed).
func newApp(t *testing.T, conn *sql.DB) *handlers.App {
	t.Helper()
	templateFuncs := template.FuncMap{"colorName": models.ColorName, "attachmentLabel": attachmentLabel, "dict": templateDict}
	templates := template.Must(template.New("root").Funcs(templateFuncs).ParseFS(webassets.FS, "templates/kiosk/*.html"))
	templates = template.Must(templates.ParseFS(webassets.FS, "templates/parent/*.html"))

	encryptor, err := util.NewEncryptor(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	app := &handlers.App{
		Cfg: &config.Config{
			CalendarWindowDays:     7,
			ApprovalLinkTTL:        time.Hour,
			InviteLinkTTL:          time.Hour,
			PasswordResetLinkTTL:   time.Hour,
			PublicBaseURL:          "http://testserver.local",
			PluginConnectionSecret: "test-plugin-connection-secret",
		},
		Users:            &models.UserStore{DB: conn},
		Sessions:         &models.SessionStore{DB: conn},
		CalendarAccounts: &models.CalendarAccountStore{DB: conn},
		Calendars:        &models.CalendarStore{DB: conn},
		Events:           &models.EventStore{DB: conn},
		Chores:           &models.ChoreStore{DB: conn},
		ChoreDefs:        &models.ChoreDefinitionStore{DB: conn},
		ChoreInstances:   &models.ChoreInstanceStore{DB: conn},
		Settings:         &models.SettingsStore{DB: conn},
		Weather:          &weather.Cache{},
		Plugins:          &models.PluginStore{DB: conn},
		Approval:         auth.NewApprovalLinkSigner("test-approval-secret"),
		Invite:           auth.NewApprovalLinkSigner("test-invite-secret"),
		PasswordReset:    auth.NewApprovalLinkSigner("test-password-reset-secret"),
		GoogleOAuthState: auth.NewApprovalLinkSigner("test-google-oauth-state-secret"),
		Encryptor:        encryptor,
		Templates:        templates,
	}
	app.SessionMgr = &auth.SessionManager{Sessions: app.Sessions, Users: app.Users, TTL: time.Hour}
	app.CSRF = auth.NewCSRFManager("test-csrf-secret")
	app.LoginLimiter = auth.NewLoginLimiter(10, 15*time.Minute)
	return app
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	conn := testutil.RequireDB(t)
	smtp := startTestFakeSMTP(t)
	smtpHost, smtpPort := smtp.addr()

	app := newApp(t, conn)
	app.Mailer = &email.Sender{Host: smtpHost, Port: smtpPort, From: "hhq@example.com"}

	srv := httptest.NewServer(buildRouter(app))
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // let tests inspect redirects themselves
		},
	}

	return &testServer{Server: srv, App: app, Client: client, SMTP: smtp}
}

// login creates a parent, logs in via the real /login flow (so session
// cookie + CSRF derivation both go through production code), and returns
// the CSRF token to attach to subsequent state-changing form submissions.
func (ts *testServer) login(t *testing.T, email, password string) (csrfToken string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := ts.App.Users.CreateParent(t.Context(), "Test Parent", email, hash); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	resp, err := ts.Client.PostForm(ts.URL+"/login", url.Values{
		"email":    {email},
		"password": {password},
	})
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	u, _ := url.Parse(ts.URL)
	var sessionToken string
	for _, c := range ts.Client.Jar.Cookies(u) {
		if c.Name == "hhq_session" {
			sessionToken = c.Value
		}
	}
	if sessionToken == "" {
		t.Fatal("expected a hhq_session cookie after login")
	}
	return ts.App.CSRF.Token(sessionToken)
}

// newBrokenDBTestServer builds a real router (same buildRouter as
// production) wired to brokenDB(t) - a *sql.DB that is open but immediately
// closed, so every query against it deterministically and instantly fails
// with "sql: database is closed" - a clean, isolated way to exercise
// handlers' DB-error branches (500s) without touching the shared per-package
// Postgres container used by every other test (this DB is a separate
// connection entirely, so closing it can't affect other tests). Built via
// the same newApp helper as newTestServer (minus Mailer, since no broken-DB
// test sends email), so callers never need to patch in a missing field
// before using it, and the two constructors can't drift out of sync again.
func newBrokenDBTestServer(t *testing.T) *testServer {
	t.Helper()
	conn := brokenDB(t)
	app := newApp(t, conn)

	srv := httptest.NewServer(buildRouter(app))
	t.Cleanup(srv.Close)
	return &testServer{Server: srv, App: app, Client: &http.Client{}}
}

func TestKioskRoutesReturnServerErrorOnDBFailure(t *testing.T) {
	ts := newBrokenDBTestServer(t)

	for _, path := range []string{"/", "/kiosk/fragments/agenda", "/kiosk/fragments/calendar", "/kiosk/fragments/chores", "/kiosk/fragments/week"} {
		resp, err := ts.Client.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("GET %s: status = %d, want %d", path, resp.StatusCode, http.StatusInternalServerError)
		}
	}
}

// TestApprovalRespondReturnsServerErrorOnDBFailure covers ApprovalRespond's
// generic (non-ErrInvalidTransition) error branch using the same
// broken-DB technique as the kiosk DB-failure tests above - the signer
// itself needs no DB, only the subsequent Decide() call does.
func TestApprovalRespondReturnsServerErrorOnDBFailure(t *testing.T) {
	ts := newBrokenDBTestServer(t)

	token := ts.App.Approval.Sign(1, auth.ActionApprove, time.Hour)
	resp, err := ts.Client.Get(ts.URL + "/approval/respond?token=" + token)
	if err != nil {
		t.Fatalf("GET /approval/respond: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestInviteAcceptPageReturnsServerErrorOnDBFailure covers
// loadPendingInvite's generic-error branch (GetByID failing with something
// other than ErrNotFound).
func TestInviteAcceptPageReturnsServerErrorOnDBFailure(t *testing.T) {
	ts := newBrokenDBTestServer(t)

	token := ts.App.Invite.Sign(1, auth.ActionInviteAccept, time.Hour)
	resp, err := ts.Client.Get(ts.URL + "/invite/accept?token=" + token)
	if err != nil {
		t.Fatalf("GET /invite/accept: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// brokenDB returns an open-then-immediately-closed *sql.DB, separate from
// any shared test container, so queries against it deterministically and
// instantly fail with "sql: database is closed".
func brokenDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("pgx", "postgres://broken:broken@127.0.0.1:1/broken?sslmode=disable")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	conn.Close()
	return conn
}

// TestParentDashboardMutationsReturnServerErrorOnStoreFailure covers several
// of the parent dashboard mutation handlers' DB-error (500) branches. It logs
// in normally (real DB), then swaps out ONE specific store on the App for a
// broken one before each sub-test - since SessionMgr captured its own
// (working) Users/Sessions store pointers at construction time, session
// verification keeps working even after ts.App.<Store> is swapped, so only
// the handler's own store call fails.
func TestParentDashboardMutationsReturnServerErrorOnStoreFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "store-failure@example.com", "s3cret-password")
	broken := brokenDB(t)

	t.Run("ParentDashboard via broken Settings store", func(t *testing.T) {
		orig := ts.App.Settings
		ts.App.Settings = &models.SettingsStore{DB: broken}
		defer func() { ts.App.Settings = orig }()

		resp, err := ts.Client.Get(ts.URL + "/parent")
		if err != nil {
			t.Fatalf("GET /parent: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
		}
	})

	t.Run("UpdateSettings via broken Settings store", func(t *testing.T) {
		orig := ts.App.Settings
		ts.App.Settings = &models.SettingsStore{DB: broken}
		defer func() { ts.App.Settings = orig }()

		resp := ts.postForm(t, "/parent/settings", csrfToken, url.Values{"app_title": {"X"}, "timezone": {"America/Chicago"}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
		}
	})

	t.Run("CreateChild via broken Users store", func(t *testing.T) {
		orig := ts.App.Users
		ts.App.Users = &models.UserStore{DB: broken}
		defer func() { ts.App.Users = orig }()

		resp := ts.postForm(t, "/parent/users/children", csrfToken, url.Values{"name": {"Kid"}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
		}
	})

	t.Run("CreateParent via broken Users store", func(t *testing.T) {
		orig := ts.App.Users
		ts.App.Users = &models.UserStore{DB: broken}
		defer func() { ts.App.Users = orig }()

		resp := ts.postForm(t, "/parent/users/parents", csrfToken, url.Values{"name": {"X"}, "email": {"broken-invite@example.com"}})
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
		}
	})

	t.Run("CreateCalendarAccount via broken CalendarAccounts store", func(t *testing.T) {
		orig := ts.App.CalendarAccounts
		ts.App.CalendarAccounts = &models.CalendarAccountStore{DB: broken}
		defer func() { ts.App.CalendarAccounts = orig }()

		resp := ts.postForm(t, "/parent/calendar-accounts", csrfToken, url.Values{
			"provider": {"caldav_generic"}, "name": {"X"}, "username": {"u"}, "password": {"p"},
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
		}
	})

	t.Run("ToggleCalendarEnabled via broken Calendars store", func(t *testing.T) {
		orig := ts.App.Calendars
		ts.App.Calendars = &models.CalendarStore{DB: broken}
		defer func() { ts.App.Calendars = orig }()

		resp := ts.postForm(t, "/parent/calendars/1/toggle", csrfToken, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
		}
	})

	t.Run("CreateChoreDefinition via broken ChoreDefs store", func(t *testing.T) {
		childID, err := ts.App.Users.CreateChild(t.Context(), "Kid For Broken Chore Test", "#3B82F6")
		if err != nil {
			t.Fatalf("CreateChild: %v", err)
		}
		orig := ts.App.ChoreDefs
		ts.App.ChoreDefs = &models.ChoreDefinitionStore{DB: broken}
		defer func() { ts.App.ChoreDefs = orig }()

		resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
			"child_id": {fmt.Sprint(childID)}, "chore_id": {"new"}, "new_chore_name": {"Dishes"}, "points": {"5"}, "kind": {"recurring"}, "days": {"1"},
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
		}
	})
}

func TestKioskIndexRendersWithoutAuth(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

func TestKioskFragmentsRenderWithoutAuth(t *testing.T) {
	ts := newTestServer(t)

	for _, path := range []string{"/kiosk/fragments/home", "/kiosk/fragments/agenda", "/kiosk/fragments/calendar", "/kiosk/fragments/chores", "/kiosk/fragments/weather", "/kiosk/weather/page"} {
		resp, err := ts.Client.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", path, resp.StatusCode)
		}
	}
}

// TestKioskFragmentWeekRendersWithoutAuth is TestKioskFragmentsRenderWithoutAuth's
// counterpart for the new 5-day view's fragment route.
func TestKioskFragmentWeekRendersWithoutAuth(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Get(ts.URL + "/kiosk/fragments/week")
	if err != nil {
		t.Fatalf("GET /kiosk/fragments/week: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestKioskIndexSeedsHomeViewWhenLayoutSettingUnset confirms the default
// (unset kiosk_layout setting) uses the 5-day calendar/week view as "Home" -
// the classic 3-column view remains reachable via the "Agenda" alternate nav
// button.
func TestKioskIndexSeedsHomeViewWhenLayoutSettingUnset(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "kiosk-week") {
		t.Errorf("expected the 5-day week grid markup when kiosk_layout is unset, got: %s", body)
	}
	if !strings.Contains(string(body), `data-nav="agenda"`) {
		t.Errorf("expected an 'Agenda' alternate nav button when weekly is the default, got: %s", body)
	}
}

// TestKioskIndexSeedsWeekViewWhenLayoutSettingIsWeekly confirms setting
// kiosk_layout=weekly makes the 5-day view "Home", with the classic view
// reachable via the "Agenda" alternate nav button instead.
func TestKioskIndexSeedsWeekViewWhenLayoutSettingIsWeekly(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	if err := ts.App.Settings.Set(ctx, "kiosk_layout", "weekly"); err != nil {
		t.Fatalf("Settings.Set: %v", err)
	}

	resp, err := ts.Client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "kiosk-week") {
		t.Errorf("expected the 5-day view markup when kiosk_layout=weekly, got: %s", body)
	}
	if !strings.Contains(string(body), `data-nav="agenda"`) {
		t.Errorf("expected an 'Agenda' alternate nav button when weekly is the default, got: %s", body)
	}
}

func TestKioskEventDetail(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	accountID, err := ts.App.CalendarAccounts.Create(ctx, models.CalendarAccount{
		Name: "Acct", Provider: models.ProviderGeneric, EncryptedPassword: []byte("x"),
	})
	if err != nil {
		t.Fatalf("CalendarAccounts.Create: %v", err)
	}
	calID, err := ts.App.Calendars.UpsertDiscovered(ctx, accountID, "/path/", "Home", "#3B82F6")
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}

	now := time.Now()
	if err := ts.App.Events.Upsert(ctx, models.Event{
		CalendarID: calID,
		UID:        "detail-1@example.com",
		Summary:    "Team meeting",
		Location:   sql.NullString{String: "Kitchen Table", Valid: true},
		Attachments: []models.Attachment{
			{URI: "https://example.com/files/agenda.pdf"},
		},
		StartsAt: now,
		EndsAt:   now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Events.Upsert: %v", err)
	}

	events, err := ts.App.Events.ListToday(ctx)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListToday: events=%+v err=%v", events, err)
	}

	resp, err := ts.Client.Get(fmt.Sprintf("%s/kiosk/events/%d", ts.URL, events[0].ID))
	if err != nil {
		t.Fatalf("GET event detail: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "Team meeting") || !strings.Contains(string(body), "Kitchen Table") {
		t.Fatalf("body = %q, want it to contain the event's summary/location", body)
	}
	// Exercises attachmentLabel's derive-a-label-from-the-URI path (round-tripped
	// through the real template FuncMap, not called directly since it's unexported).
	if !strings.Contains(string(body), "agenda.pdf") {
		t.Fatalf("body = %q, want the attachment link labeled from its filename", body)
	}
}

func TestKioskEventDetailInvalidIDReturnsBadRequest(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Get(ts.URL + "/kiosk/events/not-a-number")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestKioskEventDetailNotFoundReturns404(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Get(ts.URL + "/kiosk/events/99999")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	ts := newTestServer(t)
	resp, err := ts.Client.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestParentDashboardRedirectsToLoginWhenUnauthenticated(t *testing.T) {
	ts := newTestServer(t)
	// A parent must already exist, otherwise RequireParent redirects to /setup instead.
	if _, err := ts.App.Users.CreateParent(t.Context(), "Existing Parent", "existing@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	resp, err := ts.Client.Get(ts.URL + "/parent")
	if err != nil {
		t.Fatalf("GET /parent: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	if loc := resp.Header.Get("Location"); loc != "/login?next=/parent" {
		t.Fatalf("Location = %q, want /login?next=/parent", loc)
	}
}

func TestParentDashboardRedirectsToSetupWhenNoParentExists(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Get(ts.URL + "/parent")
	if err != nil {
		t.Fatalf("GET /parent: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	if loc := resp.Header.Get("Location"); loc != "/setup" {
		t.Fatalf("Location = %q, want /setup", loc)
	}
}

func TestSetupFlowCreatesFirstParentAndLogsIn(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.PostForm(ts.URL+"/setup", url.Values{
		"name":             {"First Parent"},
		"email":            {"first@example.com"},
		"password":         {"s3cret-password"},
		"confirm_password": {"s3cret-password"},
	})
	if err != nil {
		t.Fatalf("POST /setup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/parent" {
		t.Fatalf("status=%d location=%q, want 303 to /parent", resp.StatusCode, resp.Header.Get("Location"))
	}

	// Now logged in - /parent should render (200), not redirect.
	dashResp, err := ts.Client.Get(ts.URL + "/parent")
	if err != nil {
		t.Fatalf("GET /parent after setup: %v", err)
	}
	defer dashResp.Body.Close()
	if dashResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /parent after setup: status = %d, want 200", dashResp.StatusCode)
	}
}

func TestLoginFailureThenSuccess(t *testing.T) {
	ts := newTestServer(t)
	hash, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := ts.App.Users.CreateParent(t.Context(), "Parent", "parent@example.com", hash); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	badResp, err := ts.Client.PostForm(ts.URL+"/login", url.Values{
		"email": {"parent@example.com"}, "password": {"wrong-password"},
	})
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusOK {
		t.Fatalf("failed login: status = %d, want 200 (re-rendered form with error)", badResp.StatusCode)
	}

	goodResp, err := ts.Client.PostForm(ts.URL+"/login", url.Values{
		"email": {"parent@example.com"}, "password": {"correct-password"},
	})
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	goodResp.Body.Close()
	if goodResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("successful login: status = %d, want %d", goodResp.StatusCode, http.StatusSeeOther)
	}
}

// TestLoginRejectsDeactivatedParent is a regression test: a parent removed
// via the dashboard (soft-deleted through UserStore.Deactivate, is_active =
// FALSE) must not be able to log in with their old password - previously
// LoginSubmit didn't check IsActive at all, so deactivation only blocked the
// forgot-password flow, not login itself.
func TestLoginRejectsDeactivatedParent(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()
	hash, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	userID, err := ts.App.Users.CreateParent(ctx, "Parent", "deactivated@example.com", hash)
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	if err := ts.App.Users.Deactivate(ctx, userID); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}

	resp, err := ts.Client.PostForm(ts.URL+"/login", url.Values{
		"email": {"deactivated@example.com"}, "password": {"correct-password"},
	})
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-rendered form with error, not a successful login)", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), "Invalid email or password") {
		t.Fatalf("expected the standard invalid-credentials error, got: %s", body)
	}
}

func TestLoginRateLimiting(t *testing.T) {
	ts := newTestServer(t)
	hash, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := ts.App.Users.CreateParent(t.Context(), "Parent", "ratelimited@example.com", hash); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	// LoginLimiter allows 10 attempts per key; exhaust it with wrong passwords.
	for i := 0; i < 10; i++ {
		resp, err := ts.Client.PostForm(ts.URL+"/login", url.Values{
			"email": {"ratelimited@example.com"}, "password": {"wrong"},
		})
		if err != nil {
			t.Fatalf("login POST %d: %v", i, err)
		}
		resp.Body.Close()
	}

	// Even the CORRECT password should now be rejected due to rate limiting.
	resp, err := ts.Client.PostForm(ts.URL+"/login", url.Values{
		"email": {"ratelimited@example.com"}, "password": {"correct-password"},
	})
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rate-limited login: status = %d, want 200 (rejected without a redirect)", resp.StatusCode)
	}
}

func TestParentDashboardCSRFProtection(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "csrf-test@example.com", "s3cret-password")

	// POST without a CSRF token must be rejected.
	resp, err := ts.Client.PostForm(ts.URL+"/parent/users/children", url.Values{"name": {"Kid"}})
	if err != nil {
		t.Fatalf("POST children: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (missing CSRF token)", resp.StatusCode, http.StatusForbidden)
	}
}

func TestCreateChildViaHTMXReturnsFragment(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "child-create@example.com", "s3cret-password")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/parent/users/children", strings.NewReader(url.Values{
		"name":       {"New Kid"},
		"csrf_token": {csrfToken},
	}.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")

	resp, err := ts.Client.Do(req)
	if err != nil {
		t.Fatalf("POST children: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (htmx fragment response)", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}

	children, err := ts.App.Users.ListChildren(t.Context())
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	found := false
	for _, c := range children {
		if c.Name == "New Kid" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected the new child to be persisted")
	}

	// Regression test: the Chores card's child <select> must be refreshed
	// via an out-of-band swap alongside the primary _children fragment, or
	// the new child won't be selectable for a new chore until a manual page
	// refresh (the bug this test guards against).
	if !strings.Contains(string(body), `hx-swap-oob="true"`) {
		t.Fatal("expected an hx-swap-oob chores-card fragment in the response so the chore-def child selector picks up the new child")
	}
	if !strings.Contains(string(body), "New Kid") || strings.Count(string(body), "New Kid") < 2 {
		t.Fatal("expected the new child's name to appear both in the children list and the OOB chore-def child <select>")
	}
}

// TestKioskChoreTapToApprovalEndToEnd exercises the full user-facing flow
// described in CLAUDE.md's requirements: a child taps a chore on the kiosk
// (no auth), it goes to pending_approval and emails parents, and clicking
// the signed approval link (no login) marks it approved.
func TestKioskChoreTapToApprovalEndToEnd(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	if _, err := ts.App.Users.CreateParent(ctx, "Parent", "parent@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	childID, err := ts.App.Users.CreateChild(ctx, "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	choreID, err := ts.App.Chores.Create(ctx, "Daily chore", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}
	if _, err := ts.App.ChoreDefs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}
	if err := ts.App.ChoreInstances.EnsureForDate(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureForDate: %v", err)
	}
	instances, err := ts.App.ChoreInstances.ListForDate(ctx, time.Now())
	if err != nil || len(instances) != 1 {
		t.Fatalf("ListForDate: instances=%+v err=%v", instances, err)
	}
	instanceID := instances[0].ID

	tapResp, err := ts.Client.Post(fmt.Sprintf("%s/kiosk/chores/%d/complete", ts.URL, instanceID), "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("kiosk tap: %v", err)
	}
	tapResp.Body.Close()
	if tapResp.StatusCode != http.StatusOK {
		t.Fatalf("kiosk tap: status = %d, want 200", tapResp.StatusCode)
	}

	got, err := ts.App.ChoreInstances.GetByID(ctx, instanceID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != models.StatusPendingApproval {
		t.Fatalf("Status = %q, want %q after kiosk tap", got.Status, models.StatusPendingApproval)
	}

	// The parent notification email is fired via a goroutine (see
	// KioskCompleteChore) so the kiosk tap itself stays fast; wait for it to
	// land on the fake SMTP server before proceeding.
	select {
	case <-ts.SMTP.received:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the parent notification email")
	}

	approveToken := ts.App.Approval.Sign(instanceID, auth.ActionApprove, time.Hour)
	approveResp, err := ts.Client.Get(ts.URL + "/approval/respond?token=" + approveToken)
	if err != nil {
		t.Fatalf("approval respond: %v", err)
	}
	approveResp.Body.Close()
	if approveResp.StatusCode != http.StatusOK {
		t.Fatalf("approval respond: status = %d, want 200", approveResp.StatusCode)
	}

	final, err := ts.App.ChoreInstances.GetByID(ctx, instanceID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if final.Status != models.StatusApproved {
		t.Fatalf("Status = %q, want %q after approval link click", final.Status, models.StatusApproved)
	}
}

// TestKioskChoreTapForParentAssigneeSkipsApprovalEmail is
// TestKioskChoreTapToApprovalEndToEnd's counterpart for a chore assigned to
// a parent instead of a child: the tap should go straight to 'approved'
// via real HTTP, with no parent-notification email sent at all (no
// approval step needed for an informational-only assignment).
func TestKioskChoreTapForParentAssigneeSkipsApprovalEmail(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	parentID, err := ts.App.Users.CreateParent(ctx, "Mom Real Name", "parent-assignee@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	if err := ts.App.Users.SetDisplayName(ctx, parentID, "Mom"); err != nil {
		t.Fatalf("SetDisplayName: %v", err)
	}
	choreID, err := ts.App.Chores.Create(ctx, "Dishes", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}
	if _, err := ts.App.ChoreDefs.CreateRecurring(ctx, parentID, choreID, 0, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}
	if err := ts.App.ChoreInstances.EnsureForDate(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureForDate: %v", err)
	}
	instances, err := ts.App.ChoreInstances.ListForDate(ctx, time.Now())
	if err != nil || len(instances) != 1 {
		t.Fatalf("ListForDate: instances=%+v err=%v", instances, err)
	}
	instanceID := instances[0].ID

	tapResp, err := ts.Client.Post(fmt.Sprintf("%s/kiosk/chores/%d/complete", ts.URL, instanceID), "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("kiosk tap: %v", err)
	}
	defer tapResp.Body.Close()
	if tapResp.StatusCode != http.StatusOK {
		t.Fatalf("kiosk tap: status = %d, want 200", tapResp.StatusCode)
	}
	body, err := io.ReadAll(tapResp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), "Mom") {
		t.Fatal("kiosk chores fragment should show the parent's display name (\"Mom\"), not the real name")
	}

	got, err := ts.App.ChoreInstances.GetByID(ctx, instanceID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != models.StatusApproved {
		t.Fatalf("Status = %q, want %q immediately after the kiosk tap (no approval step for a parent assignee)", got.Status, models.StatusApproved)
	}

	// No notification email should ever be sent for an informational,
	// parent-assigned chore - confirm nothing lands on the fake SMTP server.
	select {
	case <-ts.SMTP.received:
		t.Fatal("unexpected parent-notification email sent for a parent-assigned (informational) chore")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestApprovalRespondRejectsInvalidToken(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Get(ts.URL + "/approval/respond?token=not-a-real-token")
	if err != nil {
		t.Fatalf("approval respond: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// postForm is a small helper for the mutation-handler tests below: it POSTs
// a urlencoded form with the CSRF token attached, mirroring what every real
// dashboard form does (method="POST" + a hidden csrf_token field).
func (ts *testServer) postForm(t *testing.T, path, csrfToken string, values url.Values) *http.Response {
	t.Helper()
	if values == nil {
		values = url.Values{}
	}
	values.Set("csrf_token", csrfToken)
	resp, err := ts.Client.PostForm(ts.URL+path, values)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func TestLoginPageRedirectsToSetupWhenNoParentExists(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Get(ts.URL + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/setup" {
		t.Fatalf("status=%d location=%q, want 303 to /setup", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestLoginPageRendersWhenParentExists(t *testing.T) {
	ts := newTestServer(t)
	if _, err := ts.App.Users.CreateParent(t.Context(), "Parent", "existing@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	resp, err := ts.Client.Get(ts.URL + "/login")
	if err != nil {
		t.Fatalf("GET /login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestLogoutClearsSessionAndRedirects(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "logout-test@example.com", "s3cret-password")

	resp, err := ts.Client.Get(ts.URL + "/logout")
	if err != nil {
		t.Fatalf("GET /logout: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("status=%d location=%q, want 303 to /login", resp.StatusCode, resp.Header.Get("Location"))
	}

	// The dashboard should no longer be reachable with the now-cleared session.
	dashResp, err := ts.Client.Get(ts.URL + "/parent")
	if err != nil {
		t.Fatalf("GET /parent after logout: %v", err)
	}
	defer dashResp.Body.Close()
	if dashResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET /parent after logout: status = %d, want a redirect to /login", dashResp.StatusCode)
	}
}

func TestUpdateSettingsValidAndInvalidTimezone(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "settings-test@example.com", "s3cret-password")

	goodResp := ts.postForm(t, "/parent/settings", csrfToken, url.Values{
		"app_title": {"Our Family"}, "timezone": {"America/Chicago"},
	})
	goodResp.Body.Close()
	if goodResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("valid settings update: status = %d, want %d", goodResp.StatusCode, http.StatusSeeOther)
	}
	title, err := ts.App.Settings.Get(t.Context(), "app_title", "")
	if err != nil || title != "Our Family" {
		t.Fatalf("app_title = %q, err=%v, want %q", title, err, "Our Family")
	}

	badResp := ts.postForm(t, "/parent/settings", csrfToken, url.Values{
		"app_title": {"Our Family"}, "timezone": {"Not/A/Real/Zone"},
	})
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("invalid timezone: status = %d, want %d (redirect with settings_error)", badResp.StatusCode, http.StatusSeeOther)
	}
	if loc := badResp.Header.Get("Location"); !strings.Contains(loc, "settings_error=") {
		t.Fatalf("Location = %q, want it to carry settings_error", loc)
	}
}

func TestRemoveUserChild(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "remove-child@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/users/%d/remove", childID), csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	children, err := ts.App.Users.ListChildren(t.Context())
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	for _, c := range children {
		if c.ID == childID {
			t.Fatal("expected the removed child to no longer be listed")
		}
	}
}

func TestRemoveUserRefusesToRemoveLastParent(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "last-parent@example.com", "s3cret-password")

	me, err := ts.App.Users.GetByEmail(t.Context(), "last-parent@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/users/%d/remove", me.ID), csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect with invite_error)", resp.StatusCode, http.StatusSeeOther)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "invite_error=") {
		t.Fatalf("Location = %q, want it to carry invite_error", loc)
	}

	parents, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parents) != 1 {
		t.Fatalf("expected the last parent to survive removal, parents=%+v err=%v", parents, err)
	}
}

func TestCreateParentSendsInviteEmailAndRejectsDuplicate(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "inviter@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/users/parents", csrfToken, url.Values{
		"name": {"New Parent"}, "email": {"invitee@example.com"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	select {
	case <-ts.SMTP.received:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the invite email")
	}

	dupResp := ts.postForm(t, "/parent/users/parents", csrfToken, url.Values{
		"name": {"Someone Else"}, "email": {"invitee@example.com"},
	})
	dupResp.Body.Close()
	if dupResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("duplicate invite: status = %d, want %d (redirect with invite_error)", dupResp.StatusCode, http.StatusSeeOther)
	}
	if loc := dupResp.Header.Get("Location"); !strings.Contains(loc, "invite_error=") {
		t.Fatalf("Location = %q, want it to carry invite_error", loc)
	}
}

func TestResendParentInvite(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "resend-inviter@example.com", "s3cret-password")

	invitedID, err := ts.App.Users.InviteParent(t.Context(), "Pending Parent", "pending@example.com")
	if err != nil {
		t.Fatalf("InviteParent: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/users/%d/resend-invite", invitedID), csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	select {
	case <-ts.SMTP.received:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the resent invite email")
	}
}

// TestCreateCalendarAccountLifecycle exercises create -> edit page -> update
// -> resync -> delete against a fake CalDAV server that 404s everything, so
// discovery fails fast and deterministically (the handler tolerates and logs
// discovery failures rather than erroring the request - see
// CreateCalendarAccount's comment on why discovery failure doesn't block
// account creation).
func TestCreateCalendarAccountLifecycle(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "caldav-admin@example.com", "s3cret-password")

	fakeCalDAV := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer fakeCalDAV.Close()

	createResp := ts.postForm(t, "/parent/calendar-accounts", csrfToken, url.Values{
		"provider": {"caldav_generic"}, "name": {"Test Account"},
		"caldav_url": {fakeCalDAV.URL}, "username": {"user"}, "password": {"secret"},
	})
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: status = %d, want %d", createResp.StatusCode, http.StatusSeeOther)
	}

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("ListAll: accounts=%+v err=%v", accounts, err)
	}
	accountID := accounts[0].ID

	editResp, err := ts.Client.Get(fmt.Sprintf("%s/parent/calendar-accounts/%d/edit", ts.URL, accountID))
	if err != nil {
		t.Fatalf("GET edit page: %v", err)
	}
	editResp.Body.Close()
	if editResp.StatusCode != http.StatusOK {
		t.Fatalf("edit page: status = %d, want 200", editResp.StatusCode)
	}

	updateResp := ts.postForm(t, fmt.Sprintf("/parent/calendar-accounts/%d", accountID), csrfToken, url.Values{
		"name": {"Renamed Account"}, "caldav_url": {fakeCalDAV.URL}, "username": {"user"}, "password": {""},
	})
	updateResp.Body.Close()
	if updateResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("update: status = %d, want %d", updateResp.StatusCode, http.StatusSeeOther)
	}
	renamed, err := ts.App.CalendarAccounts.GetByID(t.Context(), accountID)
	if err != nil || renamed.Name != "Renamed Account" {
		t.Fatalf("GetByID after update: account=%+v err=%v", renamed, err)
	}

	resyncResp := ts.postForm(t, fmt.Sprintf("/parent/calendar-accounts/%d/resync", accountID), csrfToken, nil)
	resyncResp.Body.Close()
	if resyncResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("resync: status = %d, want %d", resyncResp.StatusCode, http.StatusSeeOther)
	}

	deleteResp := ts.postForm(t, fmt.Sprintf("/parent/calendar-accounts/%d/delete", accountID), csrfToken, nil)
	deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: status = %d, want %d", deleteResp.StatusCode, http.StatusSeeOther)
	}
	if _, err := ts.App.CalendarAccounts.GetByID(t.Context(), accountID); err != models.ErrAccountNotFound {
		t.Fatalf("expected the account to be gone after delete, err=%v", err)
	}
}

func TestBootstrapCalendarAccountsCreatesAndUpsertsOnRerun(t *testing.T) {
	ts := newTestServer(t)

	entries := []config.CalendarAccountBootstrap{
		{Name: "Bootstrap Fastmail", Provider: "fastmail", Username: "boot@fastmail.com", Password: "pw1"},
	}

	ts.App.BootstrapCalendarAccounts(t.Context(), entries)

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("ListAll after first run: accounts=%+v err=%v", accounts, err)
	}
	first := accounts[0]
	if !first.BootstrapManaged {
		t.Fatal("expected bootstrapped account to be marked BootstrapManaged")
	}
	if first.Provider != models.ProviderFastmail || first.CalDAVURL.String != "https://caldav.fastmail.com/dav/" {
		t.Fatalf("unexpected account after first run: %+v", first)
	}

	// Re-running with a changed password should update the existing row, not
	// create a duplicate.
	entries[0].Password = "pw2"
	ts.App.BootstrapCalendarAccounts(t.Context(), entries)

	accountsAfter, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil || len(accountsAfter) != 1 {
		t.Fatalf("ListAll after second run: accounts=%+v err=%v", accountsAfter, err)
	}
	if accountsAfter[0].ID != first.ID {
		t.Fatalf("expected the same account row to be reused, got id=%d want id=%d", accountsAfter[0].ID, first.ID)
	}
	if string(accountsAfter[0].EncryptedPassword) == string(first.EncryptedPassword) {
		t.Fatal("expected the encrypted password to change after re-running bootstrap with a new password")
	}
}

func TestBootstrapCalendarAccountsRemovesEntryDroppedFromConfig(t *testing.T) {
	ts := newTestServer(t)

	entries := []config.CalendarAccountBootstrap{
		{Name: "Keep Me", Provider: "fastmail", Username: "keep@fastmail.com", Password: "pw1"},
		{Name: "Drop Me", Provider: "fastmail", Username: "drop@fastmail.com", Password: "pw2"},
	}
	ts.App.BootstrapCalendarAccounts(t.Context(), entries)

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil || len(accounts) != 2 {
		t.Fatalf("ListAll after first run: accounts=%+v err=%v", accounts, err)
	}

	// Removing an entry from the config file entirely (not just skipping it
	// in one run) must remove the corresponding row - otherwise there's no
	// way to remove a bootstrap-created account short of the database.
	ts.App.BootstrapCalendarAccounts(t.Context(), entries[:1])

	accountsAfter, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil || len(accountsAfter) != 1 {
		t.Fatalf("ListAll after removal run: accounts=%+v err=%v", accountsAfter, err)
	}
	if accountsAfter[0].Name != "Keep Me" {
		t.Fatalf("expected only %q to remain, got %+v", "Keep Me", accountsAfter)
	}
}

// TestBootstrapCalendarAccountsPreservesPluginSyntheticCalendar is a
// regression test: a plugin's synthetic calendar_accounts row (created by
// ensurePluginCalendar, see plugin_bootstrap.go) is BootstrapManaged=true
// but is never listed in calendars.json - it's reconciled against
// plugins.json instead. Before this fix, BootstrapCalendarAccounts's own
// removal loop treated it as an orphaned bootstrap entry and deleted it on
// every single startup, even though the plugin itself was still present
// (and possibly just not yet reachable/registered).
func TestBootstrapCalendarAccountsPreservesPluginSyntheticCalendar(t *testing.T) {
	ts := newTestServer(t)

	pluginAccountID, err := ts.App.CalendarAccounts.Create(t.Context(), models.CalendarAccount{
		Name:             "Plugin: Bill Tracker",
		Provider:         models.ProviderPlugin,
		BootstrapManaged: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A calendars.json that knows nothing about the plugin's synthetic
	// account (as is always the case - plugins are never listed there).
	ts.App.BootstrapCalendarAccounts(t.Context(), []config.CalendarAccountBootstrap{
		{Name: "Fastmail", Provider: "fastmail", Username: "user@fastmail.com", Password: "pw"},
	})

	account, err := ts.App.CalendarAccounts.GetByID(t.Context(), pluginAccountID)
	if err != nil {
		t.Fatalf("plugin synthetic calendar account was deleted by calendars.json reconciliation: %v", err)
	}
	if account.Provider != models.ProviderPlugin {
		t.Fatalf("unexpected provider on surviving account: %+v", account)
	}
}

func TestBootstrapCalendarAccountsSkipsNameCollisionWithUIAccount(t *testing.T) {
	ts := newTestServer(t)

	uiID, err := ts.App.CalendarAccounts.Create(t.Context(), models.CalendarAccount{
		Name: "Shared Name", Provider: models.ProviderGeneric,
		CalDAVURL: sql.NullString{String: "https://caldav.example.com/", Valid: true},
		Username:  sql.NullString{String: "user", Valid: true}, EncryptedPassword: []byte("x"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ts.App.BootstrapCalendarAccounts(t.Context(), []config.CalendarAccountBootstrap{
		{Name: "Shared Name", Provider: "fastmail", Username: "boot@fastmail.com", Password: "pw"},
	})

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("ListAll: accounts=%+v err=%v", accounts, err)
	}
	unchanged, err := ts.App.CalendarAccounts.GetByID(t.Context(), uiID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if unchanged.Provider != models.ProviderGeneric || unchanged.BootstrapManaged {
		t.Fatalf("expected the UI-created account to be left untouched, got %+v", unchanged)
	}
}

func TestBootstrapCalendarAccountsEmptyListIsNoOp(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapCalendarAccounts(t.Context(), nil)

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil || len(accounts) != 0 {
		t.Fatalf("ListAll: accounts=%+v err=%v", accounts, err)
	}
}

// TestBootstrapWeatherLocationGeocodesWhenOnlyLocationGiven covers the
// WEATHER_LOCATION-only path (no explicit coordinates), which must call
// weather.Geocode and persist its result.
func TestBootstrapWeatherLocationGeocodesWhenOnlyLocationGiven(t *testing.T) {
	ts := newTestServer(t)

	geocodeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"name":"Chicago","admin1":"Illinois","country":"United States","latitude":41.85,"longitude":-87.65}]}`))
	}))
	defer geocodeSrv.Close()
	origGeocodeURL := weather.GeocodeURL
	weather.GeocodeURL = geocodeSrv.URL
	defer func() { weather.GeocodeURL = origGeocodeURL }()

	ts.App.BootstrapWeatherLocation(t.Context(), "Chicago", 0, 0, false, "")

	name, err := ts.App.Settings.Get(t.Context(), weather.SettingLocationName, "")
	if err != nil || name != "Chicago, Illinois, United States" {
		t.Fatalf("weather_location_name = %q, err=%v", name, err)
	}
	units, err := ts.App.Settings.Get(t.Context(), weather.SettingUnits, "")
	if err != nil || units != string(weather.UnitsImperial) {
		t.Fatalf("weather_units = %q, err=%v (expected default imperial)", units, err)
	}
}

// TestBootstrapWeatherLocationSkipsGeocodeWhenCoordsGiven covers the
// WEATHER_LAT/WEATHER_LON path, which must skip weather.Geocode entirely
// (no network dependency at startup).
func TestBootstrapWeatherLocationSkipsGeocodeWhenCoordsGiven(t *testing.T) {
	ts := newTestServer(t)

	origGeocodeURL := weather.GeocodeURL
	weather.GeocodeURL = "http://127.0.0.1:0/should-not-be-called"
	defer func() { weather.GeocodeURL = origGeocodeURL }()

	ts.App.BootstrapWeatherLocation(t.Context(), "Home", 41.85, -87.65, true, string(weather.UnitsMetric))

	name, err := ts.App.Settings.Get(t.Context(), weather.SettingLocationName, "")
	if err != nil || name != "Home" {
		t.Fatalf("weather_location_name = %q, err=%v", name, err)
	}
	units, err := ts.App.Settings.Get(t.Context(), weather.SettingUnits, "")
	if err != nil || units != string(weather.UnitsMetric) {
		t.Fatalf("weather_units = %q, err=%v", units, err)
	}
}

// TestBootstrapWeatherLocationSkipsWhenAlreadyConfigured covers the "seed
// only once" behavior: a location already present (e.g. set earlier via the
// parent dashboard) must not be overwritten by a later bootstrap call, the
// same restart-safety property CALENDAR_ACCOUNTS deliberately does NOT have.
func TestBootstrapWeatherLocationSkipsWhenAlreadyConfigured(t *testing.T) {
	ts := newTestServer(t)

	if err := ts.App.Settings.Set(t.Context(), weather.SettingLocationName, "Existing Location"); err != nil {
		t.Fatalf("Set location name: %v", err)
	}
	if err := ts.App.Settings.Set(t.Context(), weather.SettingLat, "10"); err != nil {
		t.Fatalf("Set lat: %v", err)
	}
	if err := ts.App.Settings.Set(t.Context(), weather.SettingLon, "20"); err != nil {
		t.Fatalf("Set lon: %v", err)
	}

	origGeocodeURL := weather.GeocodeURL
	weather.GeocodeURL = "http://127.0.0.1:0/should-not-be-called"
	defer func() { weather.GeocodeURL = origGeocodeURL }()

	ts.App.BootstrapWeatherLocation(t.Context(), "Somewhere Else", 0, 0, false, "")

	name, err := ts.App.Settings.Get(t.Context(), weather.SettingLocationName, "")
	if err != nil || name != "Existing Location" {
		t.Fatalf("weather_location_name = %q, err=%v (expected unchanged)", name, err)
	}
}

func TestBootstrapManagedAccountRejectsEditAndDelete(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "bootstrap-admin@example.com", "s3cret-password")

	ts.App.BootstrapCalendarAccounts(t.Context(), []config.CalendarAccountBootstrap{
		{Name: "Bootstrap Fastmail", Provider: "fastmail", Username: "boot@fastmail.com", Password: "pw"},
	})
	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("ListAll: accounts=%+v err=%v", accounts, err)
	}
	id := accounts[0].ID

	editResp, err := ts.Client.Get(fmt.Sprintf("%s/parent/calendar-accounts/%d/edit", ts.URL, id))
	if err != nil {
		t.Fatalf("GET edit page: %v", err)
	}
	editResp.Body.Close()
	if editResp.StatusCode != http.StatusForbidden {
		t.Fatalf("edit page: status = %d, want %d", editResp.StatusCode, http.StatusForbidden)
	}

	updateResp := ts.postForm(t, fmt.Sprintf("/parent/calendar-accounts/%d", id), csrfToken, url.Values{
		"name": {"Hacked"}, "caldav_url": {""}, "username": {"user"}, "password": {""},
	})
	updateResp.Body.Close()
	if updateResp.StatusCode != http.StatusForbidden {
		t.Fatalf("update: status = %d, want %d", updateResp.StatusCode, http.StatusForbidden)
	}

	deleteResp := ts.postForm(t, fmt.Sprintf("/parent/calendar-accounts/%d/delete", id), csrfToken, nil)
	deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusForbidden {
		t.Fatalf("delete: status = %d, want %d", deleteResp.StatusCode, http.StatusForbidden)
	}

	stillThere, err := ts.App.CalendarAccounts.GetByID(t.Context(), id)
	if err != nil || stillThere.Name != "Bootstrap Fastmail" {
		t.Fatalf("expected the bootstrap-managed account to be unchanged, got %+v err=%v", stillThere, err)
	}
}

func TestBootstrapChildrenCreatesAndUpsertsOnRerun(t *testing.T) {
	ts := newTestServer(t)

	entries := []config.ChildBootstrap{{Name: "Bootstrap Kid", Color: "#3B82F6"}}
	ts.App.BootstrapChildren(t.Context(), entries)

	children, err := ts.App.Users.ListChildren(t.Context())
	if err != nil || len(children) != 1 {
		t.Fatalf("ListChildren after first run: children=%+v err=%v", children, err)
	}
	first := children[0]
	if !first.BootstrapManaged || first.Color != "#3B82F6" {
		t.Fatalf("unexpected child after first run: %+v", first)
	}

	// Re-running with a changed color should update the existing row, not
	// create a duplicate.
	entries[0].Color = "#EF4444"
	ts.App.BootstrapChildren(t.Context(), entries)

	childrenAfter, err := ts.App.Users.ListChildren(t.Context())
	if err != nil || len(childrenAfter) != 1 {
		t.Fatalf("ListChildren after second run: children=%+v err=%v", childrenAfter, err)
	}
	if childrenAfter[0].ID != first.ID {
		t.Fatalf("expected the same child row to be reused, got id=%d want id=%d", childrenAfter[0].ID, first.ID)
	}
	if childrenAfter[0].Color != "#EF4444" {
		t.Fatalf("expected color to be refreshed, got %q", childrenAfter[0].Color)
	}
}

func TestBootstrapChildrenRemovesEntryDroppedFromConfig(t *testing.T) {
	ts := newTestServer(t)

	entries := []config.ChildBootstrap{
		{Name: "Keep Kid", Color: "#3B82F6"},
		{Name: "Drop Kid", Color: "#EF4444"},
	}
	ts.App.BootstrapChildren(t.Context(), entries)

	children, err := ts.App.Users.ListChildren(t.Context())
	if err != nil || len(children) != 2 {
		t.Fatalf("ListChildren after first run: children=%+v err=%v", children, err)
	}

	ts.App.BootstrapChildren(t.Context(), entries[:1])

	childrenAfter, err := ts.App.Users.ListChildren(t.Context())
	if err != nil || len(childrenAfter) != 1 {
		t.Fatalf("ListChildren after removal run: children=%+v err=%v", childrenAfter, err)
	}
	if childrenAfter[0].Name != "Keep Kid" {
		t.Fatalf("expected only %q to remain, got %+v", "Keep Kid", childrenAfter)
	}
}

func TestBootstrapChildrenSkipsNameCollisionWithUIChild(t *testing.T) {
	ts := newTestServer(t)

	uiID, err := ts.App.Users.CreateChild(t.Context(), "Shared Name", "#22C55E")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Shared Name", Color: "#EF4444"}})

	children, err := ts.App.Users.ListChildren(t.Context())
	if err != nil || len(children) != 1 {
		t.Fatalf("ListChildren: children=%+v err=%v", children, err)
	}
	unchanged, err := ts.App.Users.GetByID(t.Context(), uiID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if unchanged.Color != "#22C55E" || unchanged.BootstrapManaged {
		t.Fatalf("expected the UI-created child to be left untouched, got %+v", unchanged)
	}
}

func TestBootstrapChoresCreatesAndUpsertsOnRerun(t *testing.T) {
	ts := newTestServer(t)

	entries := []config.ChoreBootstrap{{Name: "Dishes", Description: "Load and run the dishwasher"}}
	ts.App.BootstrapChores(t.Context(), entries)

	chores, err := ts.App.Chores.ListActive(t.Context())
	if err != nil || len(chores) != 1 {
		t.Fatalf("ListActive after first run: chores=%+v err=%v", chores, err)
	}
	first := chores[0]
	if !first.BootstrapManaged || first.Description.String != "Load and run the dishwasher" {
		t.Fatalf("unexpected chore after first run: %+v", first)
	}

	entries[0].Description = "Load, run, and unload the dishwasher"
	ts.App.BootstrapChores(t.Context(), entries)

	choresAfter, err := ts.App.Chores.ListActive(t.Context())
	if err != nil || len(choresAfter) != 1 {
		t.Fatalf("ListActive after second run: chores=%+v err=%v", choresAfter, err)
	}
	if choresAfter[0].ID != first.ID {
		t.Fatalf("expected the same chore row to be reused, got id=%d want id=%d", choresAfter[0].ID, first.ID)
	}
	if choresAfter[0].Description.String != "Load, run, and unload the dishwasher" {
		t.Fatalf("expected description to be refreshed, got %q", choresAfter[0].Description.String)
	}
}

func TestBootstrapChoresRemovesEntryDroppedFromConfig(t *testing.T) {
	ts := newTestServer(t)

	entries := []config.ChoreBootstrap{
		{Name: "Keep Chore", Description: "keep"},
		{Name: "Drop Chore", Description: "drop"},
	}
	ts.App.BootstrapChores(t.Context(), entries)

	chores, err := ts.App.Chores.ListActive(t.Context())
	if err != nil || len(chores) != 2 {
		t.Fatalf("ListActive after first run: chores=%+v err=%v", chores, err)
	}

	ts.App.BootstrapChores(t.Context(), entries[:1])

	choresAfter, err := ts.App.Chores.ListActive(t.Context())
	if err != nil || len(choresAfter) != 1 {
		t.Fatalf("ListActive after removal run: chores=%+v err=%v", choresAfter, err)
	}
	if choresAfter[0].Name != "Keep Chore" {
		t.Fatalf("expected only %q to remain, got %+v", "Keep Chore", choresAfter)
	}
}

func TestBootstrapChoresSkipsNameCollisionWithUIChore(t *testing.T) {
	ts := newTestServer(t)

	uiID, err := ts.App.Chores.Create(t.Context(), "Shared Chore", "original")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ts.App.BootstrapChores(t.Context(), []config.ChoreBootstrap{{Name: "Shared Chore", Description: "hijacked"}})

	unchanged, err := ts.App.Chores.GetByID(t.Context(), uiID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if unchanged.Description.String != "original" || unchanged.BootstrapManaged {
		t.Fatalf("expected the UI-created chore to be left untouched, got %+v", unchanged)
	}
}

func TestBootstrapAssignmentsCreatesAndUpsertsOnRerun(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Assignee Kid"}})
	ts.App.BootstrapChores(t.Context(), []config.ChoreBootstrap{{Name: "Trash"}})

	entries := []config.AssignmentBootstrap{
		{Child: "Assignee Kid", Chore: "Trash", Points: 5, DaysOfWeek: []string{"tue", "fri"}},
	}
	ts.App.BootstrapAssignments(t.Context(), entries)

	defs, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defs) != 1 {
		t.Fatalf("ListActive after first run: defs=%+v err=%v", defs, err)
	}
	first := defs[0]
	wantMask := models.WeekdayBit(time.Tuesday) | models.WeekdayBit(time.Friday)
	if !first.BootstrapManaged || first.Points != 5 || !first.DaysOfWeek.Valid || int(first.DaysOfWeek.Int32) != wantMask || first.OneOffDate.Valid {
		t.Fatalf("unexpected assignment after first run: %+v", first)
	}

	// Re-running with a switch to a one-off schedule and different points
	// should update the existing row (and satisfy the recurring_xor_one_off
	// constraint by clearing days_of_week), not create a duplicate.
	entries[0] = config.AssignmentBootstrap{Child: "Assignee Kid", Chore: "Trash", Points: 3, OneOffDate: "2026-08-01"}
	ts.App.BootstrapAssignments(t.Context(), entries)

	defsAfter, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defsAfter) != 1 {
		t.Fatalf("ListActive after second run: defs=%+v err=%v", defsAfter, err)
	}
	second := defsAfter[0]
	if second.ID != first.ID {
		t.Fatalf("expected the same assignment row to be reused, got id=%d want id=%d", second.ID, first.ID)
	}
	if second.Points != 3 || second.DaysOfWeek.Valid || !second.OneOffDate.Valid || second.OneOffDate.Time.Format("2006-01-02") != "2026-08-01" {
		t.Fatalf("expected assignment to be refreshed to a one-off schedule, got %+v", second)
	}
}

func TestBootstrapAssignmentsRemovesEntryDroppedFromConfig(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Assignee Kid"}})
	ts.App.BootstrapChores(t.Context(), []config.ChoreBootstrap{{Name: "Trash"}, {Name: "Dishes"}})

	entries := []config.AssignmentBootstrap{
		{Child: "Assignee Kid", Chore: "Trash", Points: 5, DaysOfWeek: []string{"tue", "fri"}},
		{Child: "Assignee Kid", Chore: "Dishes", Points: 2, DaysOfWeek: []string{"mon"}},
	}
	ts.App.BootstrapAssignments(t.Context(), entries)

	defs, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defs) != 2 {
		t.Fatalf("ListActive after first run: defs=%+v err=%v", defs, err)
	}

	ts.App.BootstrapAssignments(t.Context(), entries[:1])

	defsAfter, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defsAfter) != 1 {
		t.Fatalf("ListActive after removal run: defs=%+v err=%v", defsAfter, err)
	}
	if defsAfter[0].Name != "Trash" {
		t.Fatalf("expected only the %q assignment to remain, got %+v", "Trash", defsAfter)
	}
}

func TestBootstrapAssignmentsSkipsUnknownChildOrChore(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapAssignments(t.Context(), []config.AssignmentBootstrap{
		{Child: "Nobody", Chore: "Nothing", Points: 1, DaysOfWeek: []string{"mon"}},
	})

	defs, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defs) != 0 {
		t.Fatalf("expected no assignment to be created for an unknown child/chore, got defs=%+v err=%v", defs, err)
	}
}

func TestBootstrapManagedChildRejectsRemove(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "bootstrap-child-admin@example.com", "s3cret-password")

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Locked Kid"}})
	children, err := ts.App.Users.ListChildren(t.Context())
	if err != nil || len(children) != 1 {
		t.Fatalf("ListChildren: children=%+v err=%v", children, err)
	}
	id := children[0].ID

	resp := ts.postForm(t, fmt.Sprintf("/parent/users/%d/remove", id), csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("remove: status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}

	stillThere, err := ts.App.Users.GetByID(t.Context(), id)
	if err != nil || !stillThere.IsActive {
		t.Fatalf("expected the bootstrap-managed child to be unchanged, got %+v err=%v", stillThere, err)
	}
}

func TestBootstrapManagedChoreRejectsEditAndDelete(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "bootstrap-chore-admin@example.com", "s3cret-password")

	ts.App.BootstrapChores(t.Context(), []config.ChoreBootstrap{{Name: "Locked Chore", Description: "orig"}})
	chores, err := ts.App.Chores.ListActive(t.Context())
	if err != nil || len(chores) != 1 {
		t.Fatalf("ListActive: chores=%+v err=%v", chores, err)
	}
	id := chores[0].ID

	updateResp := ts.postForm(t, fmt.Sprintf("/parent/chores/catalog/%d", id), csrfToken, url.Values{
		"name": {"Hacked"}, "description": {"hacked"},
	})
	updateResp.Body.Close()
	if updateResp.StatusCode != http.StatusForbidden {
		t.Fatalf("update: status = %d, want %d", updateResp.StatusCode, http.StatusForbidden)
	}

	deleteResp := ts.postForm(t, fmt.Sprintf("/parent/chores/catalog/%d/deactivate", id), csrfToken, nil)
	deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusForbidden {
		t.Fatalf("deactivate: status = %d, want %d", deleteResp.StatusCode, http.StatusForbidden)
	}

	stillThere, err := ts.App.Chores.GetByID(t.Context(), id)
	if err != nil || stillThere.Name != "Locked Chore" || stillThere.Description.String != "orig" {
		t.Fatalf("expected the bootstrap-managed chore to be unchanged, got %+v err=%v", stillThere, err)
	}
}

func TestBootstrapManagedChoreDefinitionRejectsDeactivate(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "bootstrap-assignment-admin@example.com", "s3cret-password")

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Assignment Kid"}})
	ts.App.BootstrapChores(t.Context(), []config.ChoreBootstrap{{Name: "Locked Assignment Chore"}})
	ts.App.BootstrapAssignments(t.Context(), []config.AssignmentBootstrap{
		{Child: "Assignment Kid", Chore: "Locked Assignment Chore", Points: 2, DaysOfWeek: []string{"mon"}},
	})
	defs, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defs) != 1 {
		t.Fatalf("ListActive: defs=%+v err=%v", defs, err)
	}
	id := defs[0].ID

	resp := ts.postForm(t, fmt.Sprintf("/parent/chores/definitions/%d/deactivate", id), csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("deactivate: status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}

	defsAfter, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defsAfter) != 1 || !defsAfter[0].Active {
		t.Fatalf("expected the bootstrap-managed assignment to remain active, got %+v err=%v", defsAfter, err)
	}
}

func TestToggleCalendarEnabled(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "toggle-cal@example.com", "s3cret-password")

	accountID, err := ts.App.CalendarAccounts.Create(t.Context(), models.CalendarAccount{
		Name: "Acct", Provider: models.ProviderGeneric, EncryptedPassword: []byte("x"),
	})
	if err != nil {
		t.Fatalf("CalendarAccounts.Create: %v", err)
	}
	calID, err := ts.App.Calendars.UpsertDiscovered(t.Context(), accountID, "/path/", "Home", "#3B82F6")
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/calendars/%d/toggle", calID), csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	cal, err := ts.App.Calendars.GetByID(t.Context(), calID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if cal.Enabled {
		t.Fatal("expected toggling a newly-discovered (enabled-by-default) calendar to disable it")
	}
}

func TestCreateChoreDefinitionRecurringAndOneOff(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-admin@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	recurringResp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {fmt.Sprint(childID)}, "chore_id": {"new"}, "new_chore_name": {"Dishes"}, "description": {"After dinner"},
		"points": {"5"}, "kind": {"recurring"}, "days": {"1", "3", "5"},
	})
	recurringResp.Body.Close()
	if recurringResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("recurring create: status = %d, want %d", recurringResp.StatusCode, http.StatusSeeOther)
	}

	oneOffResp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {fmt.Sprint(childID)}, "chore_id": {"new"}, "new_chore_name": {"Clean garage"}, "points": {"10"},
		"kind": {"one_off"}, "one_off_date": {"2026-08-01"},
	})
	oneOffResp.Body.Close()
	if oneOffResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("one-off create: status = %d, want %d", oneOffResp.StatusCode, http.StatusSeeOther)
	}

	defs, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defs) != 2 {
		t.Fatalf("ListActive: defs=%+v err=%v", defs, err)
	}
}

func TestCreateChoreDefinitionRecurringWithNoDaysReturnsBadRequest(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-admin-2@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {fmt.Sprint(childID)}, "chore_id": {"new"}, "new_chore_name": {"Dishes"}, "points": {"5"}, "kind": {"recurring"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestDeactivateChoreDefinition(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-deactivate@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	choreID, err := ts.App.Chores.Create(t.Context(), "Dishes", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}
	defID, err := ts.App.ChoreDefs.CreateRecurring(t.Context(), childID, choreID, 5, 0b1111111)
	if err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/chores/definitions/%d/deactivate", defID), csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	defs, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	for _, d := range defs {
		if d.ID == defID {
			t.Fatal("expected the deactivated chore definition to no longer be active")
		}
	}
}

func TestParentDecideChoreApproveAndReject(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-decide@example.com", "s3cret-password")
	ctx := t.Context()

	childID, err := ts.App.Users.CreateChild(ctx, "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	choreID, err := ts.App.Chores.Create(ctx, "Dishes", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}
	defID, err := ts.App.ChoreDefs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111)
	if err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}
	_ = defID
	if err := ts.App.ChoreInstances.EnsureForDate(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureForDate: %v", err)
	}
	instances, err := ts.App.ChoreInstances.ListForDate(ctx, time.Now())
	if err != nil || len(instances) != 1 {
		t.Fatalf("ListForDate: instances=%+v err=%v", instances, err)
	}
	instanceID := instances[0].ID
	if _, err := ts.App.ChoreInstances.MarkComplete(ctx, instanceID); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}

	approveResp := ts.postForm(t, fmt.Sprintf("/parent/chores/%d/decide", instanceID), csrfToken, url.Values{"decision": {"approve"}})
	approveResp.Body.Close()
	if approveResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("approve: status = %d, want %d", approveResp.StatusCode, http.StatusSeeOther)
	}
	got, err := ts.App.ChoreInstances.GetByID(ctx, instanceID)
	if err != nil || got.Status != models.StatusApproved {
		t.Fatalf("after approve: instance=%+v err=%v", got, err)
	}
}

func TestParentResetRejectedChore(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-reset@example.com", "s3cret-password")
	ctx := t.Context()

	childID, err := ts.App.Users.CreateChild(ctx, "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	choreID, err := ts.App.Chores.Create(ctx, "Dishes", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}
	if _, err := ts.App.ChoreDefs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}
	if err := ts.App.ChoreInstances.EnsureForDate(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureForDate: %v", err)
	}
	instances, err := ts.App.ChoreInstances.ListForDate(ctx, time.Now())
	if err != nil || len(instances) != 1 {
		t.Fatalf("ListForDate: instances=%+v err=%v", instances, err)
	}
	instanceID := instances[0].ID
	if _, err := ts.App.ChoreInstances.MarkComplete(ctx, instanceID); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	if err := ts.App.ChoreInstances.Decide(ctx, instanceID, false, 0); err != nil {
		t.Fatalf("Decide(reject): %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/chores/%d/reset", instanceID), csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	got, err := ts.App.ChoreInstances.GetByID(ctx, instanceID)
	if err != nil || got.Status != models.StatusIncomplete {
		t.Fatalf("after reset: instance=%+v err=%v", got, err)
	}
}

func TestWeeklyReportDownload(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "report-viewer@example.com", "s3cret-password")

	resp, err := ts.Client.Get(ts.URL + "/parent/report")
	if err != nil {
		t.Fatalf("GET /parent/report: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.HasPrefix(string(body), "%PDF-") {
		t.Fatalf("expected a PDF body, got %d bytes starting %q", len(body), string(body[:min(20, len(body))]))
	}
}

func TestInviteAcceptFlow(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	userID, err := ts.App.Users.InviteParent(ctx, "Invitee", "invitee-accept@example.com")
	if err != nil {
		t.Fatalf("InviteParent: %v", err)
	}
	token := ts.App.Invite.Sign(userID, auth.ActionInviteAccept, time.Hour)

	pageResp, err := ts.Client.Get(ts.URL + "/invite/accept?token=" + token)
	if err != nil {
		t.Fatalf("GET /invite/accept: %v", err)
	}
	pageResp.Body.Close()
	if pageResp.StatusCode != http.StatusOK {
		t.Fatalf("invite page: status = %d, want 200", pageResp.StatusCode)
	}

	submitResp, err := ts.Client.PostForm(ts.URL+"/invite/accept", url.Values{
		"token": {token}, "password": {"a-new-password"}, "confirm_password": {"a-new-password"},
	})
	if err != nil {
		t.Fatalf("POST /invite/accept: %v", err)
	}
	defer submitResp.Body.Close()
	if submitResp.StatusCode != http.StatusSeeOther || submitResp.Header.Get("Location") != "/parent" {
		t.Fatalf("status=%d location=%q, want 303 to /parent", submitResp.StatusCode, submitResp.Header.Get("Location"))
	}

	user, err := ts.App.Users.GetByID(ctx, userID)
	if err != nil || !user.PasswordHash.Valid {
		t.Fatalf("expected the invited user to have a password set, user=%+v err=%v", user, err)
	}
}

func TestInviteAcceptRejectsInvalidToken(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Get(ts.URL + "/invite/accept?token=not-a-real-token")
	if err != nil {
		t.Fatalf("GET /invite/accept: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (error rendered in-page)", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), "invalid or has expired") {
		t.Fatalf("expected an invalid/expired message in the body, got: %s", body)
	}
}

func TestForgotPasswordSendsResetEmailForKnownParentOnly(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	if _, err := ts.App.Users.CreateParent(ctx, "Parent", "parent-reset@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	resp, err := ts.Client.PostForm(ts.URL+"/forgot-password", url.Values{"email": {"parent-reset@example.com"}})
	if err != nil {
		t.Fatalf("POST /forgot-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), "you'll receive an email") {
		t.Fatalf("expected the generic confirmation message, got: %s", body)
	}

	select {
	case <-ts.SMTP.received:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the password reset email")
	}

	// An unknown email must render the exact same generic response, and must
	// NOT send any email - otherwise this endpoint would leak which emails
	// have parent accounts.
	unknownResp, err := ts.Client.PostForm(ts.URL+"/forgot-password", url.Values{"email": {"nobody-here@example.com"}})
	if err != nil {
		t.Fatalf("POST /forgot-password (unknown email): %v", err)
	}
	defer unknownResp.Body.Close()
	unknownBody, err := io.ReadAll(unknownResp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(unknownBody) != string(body) {
		t.Fatalf("response for unknown email differs from known email, would leak account existence")
	}
	select {
	case <-ts.SMTP.received:
		t.Fatal("expected no email to be sent for an unknown email address")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestResetPasswordFlowEndToEnd(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	userID, err := ts.App.Users.CreateParent(ctx, "Parent", "reset-flow@example.com", "old-hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	token := ts.App.PasswordReset.SignWithContext(userID, auth.ActionPasswordReset, time.Hour, "old-hash")

	pageResp, err := ts.Client.Get(ts.URL + "/reset-password?token=" + token)
	if err != nil {
		t.Fatalf("GET /reset-password: %v", err)
	}
	pageResp.Body.Close()
	if pageResp.StatusCode != http.StatusOK {
		t.Fatalf("reset page: status = %d, want 200", pageResp.StatusCode)
	}

	submitResp, err := ts.Client.PostForm(ts.URL+"/reset-password", url.Values{
		"token": {token}, "password": {"a-new-password"}, "confirm_password": {"a-new-password"},
	})
	if err != nil {
		t.Fatalf("POST /reset-password: %v", err)
	}
	defer submitResp.Body.Close()
	if submitResp.StatusCode != http.StatusSeeOther || submitResp.Header.Get("Location") != "/parent" {
		t.Fatalf("status=%d location=%q, want 303 to /parent", submitResp.StatusCode, submitResp.Header.Get("Location"))
	}

	user, err := ts.App.Users.GetByID(ctx, userID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !auth.CheckPasswordTiming(user.PasswordHash.String, "a-new-password") {
		t.Fatal("expected the new password to verify against the stored hash")
	}
}

func TestResetPasswordRejectsInvalidToken(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Get(ts.URL + "/reset-password?token=not-a-real-token")
	if err != nil {
		t.Fatalf("GET /reset-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (error rendered in-page)", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), "invalid or has expired") {
		t.Fatalf("expected an invalid/expired message in the body, got: %s", body)
	}
}

func TestResetPasswordRejectsMismatchedPasswords(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	userID, err := ts.App.Users.CreateParent(ctx, "Parent", "reset-mismatch@example.com", "old-hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	token := ts.App.PasswordReset.SignWithContext(userID, auth.ActionPasswordReset, time.Hour, "old-hash")

	resp, err := ts.Client.PostForm(ts.URL+"/reset-password", url.Values{
		"token": {token}, "password": {"a-new-password"}, "confirm_password": {"does-not-match"},
	})
	if err != nil {
		t.Fatalf("POST /reset-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (error rendered in-page)", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), "do not match") {
		t.Fatalf("expected a passwords-do-not-match message in the body, got: %s", body)
	}

	user, err := ts.App.Users.GetByID(ctx, userID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if user.PasswordHash.String != "old-hash" {
		t.Fatal("expected the password to remain unchanged after a mismatched submission")
	}
}

// TestResetPasswordTokenCannotBeReplayed is a regression test: a reset token
// is bound to the password hash that was current when it was issued
// (VerifyWithContext), so once it's been used successfully once, replaying
// the exact same token again must fail rather than silently overwriting the
// password a second time.
func TestResetPasswordTokenCannotBeReplayed(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	userID, err := ts.App.Users.CreateParent(ctx, "Parent", "reset-replay@example.com", "old-hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	token := ts.App.PasswordReset.SignWithContext(userID, auth.ActionPasswordReset, time.Hour, "old-hash")

	firstResp, err := ts.Client.PostForm(ts.URL+"/reset-password", url.Values{
		"token": {token}, "password": {"first-new-password"}, "confirm_password": {"first-new-password"},
	})
	if err != nil {
		t.Fatalf("POST /reset-password (first use): %v", err)
	}
	firstResp.Body.Close()
	if firstResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("first use: status = %d, want %d", firstResp.StatusCode, http.StatusSeeOther)
	}

	replayResp, err := ts.Client.PostForm(ts.URL+"/reset-password", url.Values{
		"token": {token}, "password": {"second-new-password"}, "confirm_password": {"second-new-password"},
	})
	if err != nil {
		t.Fatalf("POST /reset-password (replay): %v", err)
	}
	defer replayResp.Body.Close()
	if replayResp.StatusCode != http.StatusOK {
		t.Fatalf("replay: status = %d, want 200 (error rendered in-page)", replayResp.StatusCode)
	}
	replayBody, err := io.ReadAll(replayResp.Body)
	if err != nil {
		t.Fatalf("reading replay body: %v", err)
	}
	if !strings.Contains(string(replayBody), "invalid or has expired") {
		t.Fatalf("expected the replayed token to be rejected as invalid, got: %s", replayBody)
	}

	user, err := ts.App.Users.GetByID(ctx, userID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !auth.CheckPasswordTiming(user.PasswordHash.String, "first-new-password") {
		t.Fatal("expected the password to still be the one set by the first, legitimate use of the token")
	}
	if auth.CheckPasswordTiming(user.PasswordHash.String, "second-new-password") {
		t.Fatal("replaying the token must not have been able to overwrite the password a second time")
	}
}

// TestResetPasswordInvalidatesExistingSessions is a regression test: a
// session created before a password reset (e.g. an attacker's, if the reset
// was prompted by a suspected compromise) must stop working once the reset
// completes.
func TestResetPasswordInvalidatesExistingSessions(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	userID, err := ts.App.Users.CreateParent(ctx, "Parent", "reset-session@example.com", "old-hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	// Simulate a pre-existing session (e.g. an attacker's) via a second
	// client with its own cookie jar, authenticated directly against the
	// session store rather than via /login (this test only cares about
	// session-invalidation behavior, not login itself).
	attackerJar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	attackerClient := &http.Client{
		Jar: attackerJar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	sessionToken, err := ts.App.Sessions.Create(ctx, userID, time.Hour)
	if err != nil {
		t.Fatalf("Sessions.Create: %v", err)
	}
	u, _ := url.Parse(ts.URL)
	attackerJar.SetCookies(u, []*http.Cookie{{Name: "hhq_session", Value: sessionToken}})

	preResp, err := attackerClient.Get(ts.URL + "/parent")
	if err != nil {
		t.Fatalf("GET /parent (pre-reset): %v", err)
	}
	preResp.Body.Close()
	if preResp.StatusCode != http.StatusOK {
		t.Fatalf("pre-reset: status = %d, want 200 (session should still be valid)", preResp.StatusCode)
	}

	token := ts.App.PasswordReset.SignWithContext(userID, auth.ActionPasswordReset, time.Hour, "old-hash")
	resetResp, err := ts.Client.PostForm(ts.URL+"/reset-password", url.Values{
		"token": {token}, "password": {"a-new-password"}, "confirm_password": {"a-new-password"},
	})
	if err != nil {
		t.Fatalf("POST /reset-password: %v", err)
	}
	resetResp.Body.Close()
	if resetResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("reset: status = %d, want %d", resetResp.StatusCode, http.StatusSeeOther)
	}

	postResp, err := attackerClient.Get(ts.URL + "/parent")
	if err != nil {
		t.Fatalf("GET /parent (post-reset): %v", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("post-reset: status = %d, want %d (pre-existing session should be invalidated)", postResp.StatusCode, http.StatusSeeOther)
	}
	if loc := postResp.Header.Get("Location"); loc != "/login?next=/parent" {
		t.Fatalf("post-reset Location = %q, want /login?next=/parent", loc)
	}
}

// TestKioskCompleteChoreWeekContextRendersDayFragment confirms a chore
// tapped from the 5-day view (?view=week&day=...) re-renders just that one
// day's kiosk/_week_day_chores fragment (identified by its
// #week-chores-<date> id) instead of the classic view's kiosk/_chores.
func TestKioskCompleteChoreWeekContextRendersDayFragment(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	childID, err := ts.App.Users.CreateChild(ctx, "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	choreID, err := ts.App.Chores.Create(ctx, "Dishes", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}
	if _, err := ts.App.ChoreDefs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}
	today := time.Now()
	if err := ts.App.ChoreInstances.EnsureForDate(ctx, today); err != nil {
		t.Fatalf("EnsureForDate: %v", err)
	}
	instances, err := ts.App.ChoreInstances.ListForDate(ctx, today)
	if err != nil || len(instances) != 1 {
		t.Fatalf("ListForDate: instances=%+v err=%v", instances, err)
	}
	instanceID := instances[0].ID
	dateKey := today.Format("2006-01-02")

	resp, err := ts.Client.Post(
		fmt.Sprintf("%s/kiosk/chores/%d/complete?view=week&day=%s", ts.URL, instanceID, dateKey),
		"application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("kiosk tap: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), fmt.Sprintf(`id="week-chores-%s"`, dateKey)) {
		t.Fatalf("body = %q, want the day-scoped week-chores fragment, not the classic _chores fragment", body)
	}

	got, err := ts.App.ChoreInstances.GetByID(ctx, instanceID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != models.StatusPendingApproval {
		t.Fatalf("Status = %q, want %q after kiosk week-view tap", got.Status, models.StatusPendingApproval)
	}
}

func TestKioskCompleteChoreDoubleTapIsANoOp(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	childID, err := ts.App.Users.CreateChild(ctx, "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	choreID, err := ts.App.Chores.Create(ctx, "Dishes", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}
	if _, err := ts.App.ChoreDefs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}
	if err := ts.App.ChoreInstances.EnsureForDate(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureForDate: %v", err)
	}
	instances, err := ts.App.ChoreInstances.ListForDate(ctx, time.Now())
	if err != nil || len(instances) != 1 {
		t.Fatalf("ListForDate: instances=%+v err=%v", instances, err)
	}
	instanceID := instances[0].ID

	first, err := ts.Client.Post(fmt.Sprintf("%s/kiosk/chores/%d/complete", ts.URL, instanceID), "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("first tap: %v", err)
	}
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first tap: status = %d, want 200", first.StatusCode)
	}

	// Second tap on an already-pending instance must be a harmless no-op, not
	// an error - see KioskCompleteChore's ErrInvalidTransition handling.
	second, err := ts.Client.Post(fmt.Sprintf("%s/kiosk/chores/%d/complete", ts.URL, instanceID), "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("second tap: %v", err)
	}
	second.Body.Close()
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second (double) tap: status = %d, want 200 (no-op, not an error)", second.StatusCode)
	}
}

func TestKioskCompleteChoreInvalidIDReturnsBadRequest(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Post(ts.URL+"/kiosk/chores/not-a-number/complete", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestSetupPageRendersWhenNoParentExists(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Get(ts.URL + "/setup")
	if err != nil {
		t.Fatalf("GET /setup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestSetupPageRedirectsToLoginWhenAParentAlreadyExists(t *testing.T) {
	ts := newTestServer(t)
	if _, err := ts.App.Users.CreateParent(t.Context(), "Existing", "existing-setup@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	resp, err := ts.Client.Get(ts.URL + "/setup")
	if err != nil {
		t.Fatalf("GET /setup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("status=%d location=%q, want 303 to /login", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// TestSetupSubmitRejectsMalformedForm covers SetupSubmit's r.ParseForm()
// error branch - reachable here since /setup has no CSRF middleware in
// front of it to consume the error first (unlike the /parent/* routes).
func TestSetupSubmitRejectsMalformedForm(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Post(ts.URL+"/setup?%zz", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /setup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestInviteAcceptSubmitRejectsMalformedForm(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.Post(ts.URL+"/invite/accept?%zz", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /invite/accept: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestSetupSubmitRejectsMismatchedPasswords(t *testing.T) {
	ts := newTestServer(t)

	resp, err := ts.Client.PostForm(ts.URL+"/setup", url.Values{
		"name": {"First Parent"}, "email": {"mismatch@example.com"},
		"password": {"one-password"}, "confirm_password": {"another-password"},
	})
	if err != nil {
		t.Fatalf("POST /setup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (error re-rendered in-page)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "do not match") {
		t.Fatalf("expected a password-mismatch message in the body, got: %s", body)
	}

	parents, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parents) != 0 {
		t.Fatalf("expected no parent to be created, parents=%+v err=%v", parents, err)
	}
}

// TestCreateChildViaPlainFormRedirects covers CreateChild's non-htmx branch
// (the earlier TestCreateChildViaHTMXReturnsFragment only exercises the
// HX-Request path through respondAfterMutation).
func TestCreateChildViaPlainFormRedirects(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "plain-child-create@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/users/children", csrfToken, url.Values{"name": {"Plain Kid"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/parent" {
		t.Fatalf("status=%d location=%q, want 303 to /parent", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// TestUpdateSettingsHTMXReturnsFragment covers UpdateSettings' htmx branch
// (TestUpdateSettingsValidAndInvalidTimezone only exercises the plain-form
// redirect branch).
func TestUpdateSettingsHTMXReturnsFragment(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "settings-htmx@example.com", "s3cret-password")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/parent/settings", strings.NewReader(url.Values{
		"app_title": {"HTMX Family"}, "timezone": {"America/Chicago"}, "csrf_token": {csrfToken},
	}.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")

	resp, err := ts.Client.Do(req)
	if err != nil {
		t.Fatalf("POST settings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (htmx fragment)", resp.StatusCode)
	}
}

// TestUpdateSettingsHTMXInvalidTimezoneRendersErrorFragment covers
// respondSettingsError's htmx branch.
func TestUpdateSettingsHTMXInvalidTimezoneRendersErrorFragment(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "settings-htmx-err@example.com", "s3cret-password")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/parent/settings", strings.NewReader(url.Values{
		"app_title": {"HTMX Family"}, "timezone": {"Not/A/Zone"}, "csrf_token": {csrfToken},
	}.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")

	resp, err := ts.Client.Do(req)
	if err != nil {
		t.Fatalf("POST settings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (htmx error fragment)", resp.StatusCode)
	}
}

// TestCreateParentHTMXDuplicateRendersErrorFragment covers
// respondParentsError's htmx branch.
func TestCreateParentHTMXDuplicateRendersErrorFragment(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "htmx-inviter@example.com", "s3cret-password")

	if _, err := ts.App.Users.InviteParent(t.Context(), "Existing Invitee", "dup-htmx@example.com"); err != nil {
		t.Fatalf("InviteParent: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/parent/users/parents", strings.NewReader(url.Values{
		"name": {"Someone"}, "email": {"dup-htmx@example.com"}, "csrf_token": {csrfToken},
	}.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")

	resp, err := ts.Client.Do(req)
	if err != nil {
		t.Fatalf("POST parents: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (htmx error fragment)", resp.StatusCode)
	}
}

func TestResendParentInviteNotFoundAndAlreadyAccepted(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "resend-edge@example.com", "s3cret-password")

	notFoundResp := ts.postForm(t, "/parent/users/99999/resend-invite", csrfToken, nil)
	notFoundResp.Body.Close()
	if notFoundResp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown id: status = %d, want %d", notFoundResp.StatusCode, http.StatusNotFound)
	}

	me, err := ts.App.Users.GetByEmail(t.Context(), "resend-edge@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	alreadyAcceptedResp := ts.postForm(t, fmt.Sprintf("/parent/users/%d/resend-invite", me.ID), csrfToken, nil)
	alreadyAcceptedResp.Body.Close()
	if alreadyAcceptedResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("already-accepted parent: status = %d, want %d", alreadyAcceptedResp.StatusCode, http.StatusBadRequest)
	}
}

func TestRemoveUserNotFound(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "remove-notfound@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/users/99999/remove", csrfToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestEditCalendarAccountPageNotFound(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "edit-cal-notfound@example.com", "s3cret-password")

	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/99999/edit")
	if err != nil {
		t.Fatalf("GET edit page: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// TestBadIDParamsReturnBadRequest covers the invalid-id (non-numeric chi
// URL param) guard clause shared by every id-taking parent mutation route.
func TestBadIDParamsReturnBadRequest(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "bad-id@example.com", "s3cret-password")

	paths := []string{
		"/parent/users/not-a-number/resend-invite",
		"/parent/users/not-a-number/remove",
		"/parent/calendar-accounts/not-a-number/delete",
		"/parent/calendar-accounts/not-a-number/resync",
		"/parent/calendars/not-a-number/toggle",
		"/parent/chores/definitions/not-a-number/deactivate",
		"/parent/chores/not-a-number/decide",
		"/parent/chores/not-a-number/reset",
	}
	for _, p := range paths {
		resp := ts.postForm(t, p, csrfToken, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST %s: status = %d, want %d", p, resp.StatusCode, http.StatusBadRequest)
		}
	}

	editResp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/not-a-number/edit")
	if err != nil {
		t.Fatalf("GET edit page: %v", err)
	}
	editResp.Body.Close()
	if editResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET edit (bad id): status = %d, want %d", editResp.StatusCode, http.StatusBadRequest)
	}

	updateResp := ts.postForm(t, "/parent/calendar-accounts/not-a-number", csrfToken, url.Values{"name": {"x"}})
	updateResp.Body.Close()
	if updateResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST update (bad id): status = %d, want %d", updateResp.StatusCode, http.StatusBadRequest)
	}
}

// TestApprovalRespondAlreadyHandledIsNotAnError covers ApprovalRespond's
// ErrInvalidTransition branch: clicking an approve/reject link a second time
// (e.g. both parents click, or the same link twice) must render a friendly
// "already handled" page rather than erroring.
func TestApprovalRespondAlreadyHandledIsNotAnError(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	childID, err := ts.App.Users.CreateChild(ctx, "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	choreID, err := ts.App.Chores.Create(ctx, "Dishes", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}
	if _, err := ts.App.ChoreDefs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}
	if err := ts.App.ChoreInstances.EnsureForDate(ctx, time.Now()); err != nil {
		t.Fatalf("EnsureForDate: %v", err)
	}
	instances, err := ts.App.ChoreInstances.ListForDate(ctx, time.Now())
	if err != nil || len(instances) != 1 {
		t.Fatalf("ListForDate: instances=%+v err=%v", instances, err)
	}
	instanceID := instances[0].ID
	if _, err := ts.App.ChoreInstances.MarkComplete(ctx, instanceID); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}

	token := ts.App.Approval.Sign(instanceID, auth.ActionApprove, time.Hour)

	first, err := ts.Client.Get(ts.URL + "/approval/respond?token=" + token)
	if err != nil {
		t.Fatalf("first click: %v", err)
	}
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first click: status = %d, want 200", first.StatusCode)
	}

	second, err := ts.Client.Get(ts.URL + "/approval/respond?token=" + token)
	if err != nil {
		t.Fatalf("second click: %v", err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second click: status = %d, want 200 (already handled, not an error)", second.StatusCode)
	}
	body, _ := io.ReadAll(second.Body)
	if !strings.Contains(string(body), "Already handled") {
		t.Fatalf("expected an 'Already handled' message, got: %s", body)
	}
}

func TestInviteAcceptSubmitValidationErrors(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	userID, err := ts.App.Users.InviteParent(ctx, "Invitee", "invite-validation@example.com")
	if err != nil {
		t.Fatalf("InviteParent: %v", err)
	}
	token := ts.App.Invite.Sign(userID, auth.ActionInviteAccept, time.Hour)

	emptyResp, err := ts.Client.PostForm(ts.URL+"/invite/accept", url.Values{"token": {token}, "password": {""}, "confirm_password": {""}})
	if err != nil {
		t.Fatalf("POST empty password: %v", err)
	}
	emptyBody, _ := io.ReadAll(emptyResp.Body)
	emptyResp.Body.Close()
	if emptyResp.StatusCode != http.StatusOK || !strings.Contains(string(emptyBody), "required") {
		t.Fatalf("empty password: status=%d body=%s, want 200 with a 'required' message", emptyResp.StatusCode, emptyBody)
	}

	mismatchResp, err := ts.Client.PostForm(ts.URL+"/invite/accept", url.Values{"token": {token}, "password": {"a-password"}, "confirm_password": {"different"}})
	if err != nil {
		t.Fatalf("POST mismatched password: %v", err)
	}
	mismatchBody, _ := io.ReadAll(mismatchResp.Body)
	mismatchResp.Body.Close()
	if mismatchResp.StatusCode != http.StatusOK || !strings.Contains(string(mismatchBody), "do not match") {
		t.Fatalf("mismatched password: status=%d body=%s, want 200 with a 'do not match' message", mismatchResp.StatusCode, mismatchBody)
	}
}

func TestInviteAcceptAlreadyAcceptedRejectsReuse(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	hash, err := auth.HashPassword("already-set")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	userID, err := ts.App.Users.CreateParent(ctx, "Already Accepted", "already-accepted@example.com", hash)
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	token := ts.App.Invite.Sign(userID, auth.ActionInviteAccept, time.Hour)

	resp, err := ts.Client.Get(ts.URL + "/invite/accept?token=" + token)
	if err != nil {
		t.Fatalf("GET /invite/accept: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "already been accepted") {
		t.Fatalf("expected an 'already been accepted' message, got: %s", body)
	}
}

func TestWeeklyReportDownloadWithExplicitWeekStart(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "report-week-start@example.com", "s3cret-password")

	resp, err := ts.Client.Get(ts.URL + "/parent/report?week_start=2026-06-01")
	if err != nil {
		t.Fatalf("GET /parent/report: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestCreateCalendarAccountDiscoversRealCalendars covers CreateCalendarAccount's
// discovery-succeeds branch (TestCreateCalendarAccountLifecycle only exercises
// the discovery-fails-and-is-tolerated branch against a 404 fake server).
func TestCreateCalendarAccountDiscoversRealCalendars(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "caldav-discover@example.com", "s3cret-password")

	const principal = "/principals/user/"
	const homeSet = "/calendars/user/"
	const calPath = homeSet + "home/"
	fakeCalDAV := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			fmt.Fprintf(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:"><D:response><D:href>/</D:href><D:propstat><D:prop><D:current-user-principal><D:href>%s</D:href></D:current-user-principal></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`, principal)
		case r.Method == "PROPFIND" && r.URL.Path == principal:
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			fmt.Fprintf(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:response><D:href>%s</D:href><D:propstat><D:prop><C:calendar-home-set><D:href>%s</D:href></C:calendar-home-set></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`, principal, homeSet)
		case r.Method == "PROPFIND" && r.URL.Path == homeSet:
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			fmt.Fprintf(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:response><D:href>%s</D:href><D:propstat><D:prop><D:resourcetype><D:collection/><C:calendar/></D:resourcetype><D:displayname>Home</D:displayname></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`, calPath)
		case r.Method == "REPORT" && r.URL.Path == calPath:
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(207)
			fmt.Fprint(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:"></D:multistatus>`)
		default:
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer fakeCalDAV.Close()

	resp := ts.postForm(t, "/parent/calendar-accounts", csrfToken, url.Values{
		"provider": {"caldav_generic"}, "name": {"Discoverable Account"},
		"caldav_url": {fakeCalDAV.URL}, "username": {"user"}, "password": {"secret"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	accounts, err := ts.App.CalendarAccounts.ListAll(t.Context())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("ListAll: accounts=%+v err=%v", accounts, err)
	}

	// Discovery is synchronous in CreateCalendarAccount, so the calendar row
	// should already exist by the time the redirect response comes back.
	cals, err := ts.App.Calendars.ListForAccount(t.Context(), accounts[0].ID)
	if err != nil || len(cals) != 1 || cals[0].Name != "Home" {
		t.Fatalf("ListForAccount: cals=%+v err=%v", cals, err)
	}
}

// TestKioskWeatherFragmentsWithPopulatedCache covers the "cache has data"
// branch of the kiosk weather fragment/page handlers (the empty-cache
// branch is already covered by TestKioskFragmentsRenderWithoutAuth) - both
// the header quick-glance widget (icon+temp) and the full-page weather view
// should render the cached forecast's data.
// TestKioskFragmentWeekShowsForecastOnNonTodayHeadingsOnly confirms each day
// heading in the 5-day view shows a forecast icon+high-temp for every day
// except Today (which already has the header's own current-conditions
// widget - see kiosk_week.go's buildKioskWeekViewData).
func TestKioskFragmentWeekShowsForecastOnNonTodayHeadingsOnly(t *testing.T) {
	ts := newTestServer(t)
	now := time.Now()
	ts.App.Weather.Set(&weather.Forecast{
		FetchedAt: now,
		Daily: []weather.DayPoint{
			{Date: now, Code: 0, TempMax: 91},
			{Date: now.AddDate(0, 0, 1), Code: 61, TempMax: 68},
			{Date: now.AddDate(0, 0, 2), Code: 71, TempMax: 30},
		},
	})

	resp, err := ts.Client.Get(ts.URL + "/kiosk/fragments/week")
	if err != nil {
		t.Fatalf("GET /kiosk/fragments/week: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if strings.Contains(string(body), "91°") {
		t.Errorf("Today's heading should not show a forecast temp, got body: %s", body)
	}
	if !strings.Contains(string(body), "68°") {
		t.Errorf("Tomorrow's heading should show its forecast high (68°), got body: %s", body)
	}
	if !strings.Contains(string(body), "30°") {
		t.Errorf("Day-after-tomorrow's heading should show its forecast high (30°), got body: %s", body)
	}
}

func TestKioskWeatherFragmentsWithPopulatedCache(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Weather.Set(&weather.Forecast{
		FetchedAt:   time.Now(),
		CurrentTemp: 71.5,
		CurrentCode: 1,
		Hourly:      []weather.HourPoint{{Time: time.Now().Add(time.Hour), Code: 61, Temp: 65, PrecipProbability: 40}},
		Daily:       []weather.DayPoint{{Date: time.Now(), Code: 0, TempMax: 80, TempMin: 60, PrecipProbability: 10}},
	})

	resp, err := ts.Client.Get(ts.URL + "/kiosk/fragments/weather")
	if err != nil {
		t.Fatalf("GET fragments/weather: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "72") { // 71.5 rounds to 72
		t.Errorf("expected rendered temp near 72, got body: %s", body)
	}

	resp, err = ts.Client.Get(ts.URL + "/kiosk/weather/page")
	if err != nil {
		t.Fatalf("GET weather/page: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Mainly clear") {
		t.Errorf("expected current conditions description in the weather page, got body: %s", body)
	}
	if !strings.Contains(string(body), "40%") {
		t.Errorf("expected hourly precipitation probability in the weather page, got body: %s", body)
	}
}

// TestUpdateSettingsGeocodesLocationAndPersists covers UpdateSettings'
// location_query -> weather.Geocode -> settings-save path, including the
// immediate refreshWeatherAsync call populating the cache without waiting
// for the scheduler's next tick.
func TestUpdateSettingsGeocodesLocationAndPersists(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "weather-settings@example.com", "s3cret-password")

	geocodeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"name":"Chicago","admin1":"Illinois","country":"United States","latitude":41.85,"longitude":-87.65}]}`))
	}))
	defer geocodeSrv.Close()
	origGeocodeURL := weather.GeocodeURL
	weather.GeocodeURL = geocodeSrv.URL
	defer func() { weather.GeocodeURL = origGeocodeURL }()

	forecastSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"current_weather":{"temperature":50,"weathercode":3},"hourly":{"time":[],"temperature_2m":[],"precipitation_probability":[],"weathercode":[]},"daily":{"time":[],"weathercode":[],"temperature_2m_max":[],"temperature_2m_min":[],"precipitation_probability_max":[]}}`))
	}))
	defer forecastSrv.Close()
	origForecastURL := weather.ForecastURL
	weather.ForecastURL = forecastSrv.URL
	defer func() { weather.ForecastURL = origForecastURL }()

	resp := ts.postForm(t, "/parent/settings", csrfToken, url.Values{
		"app_title": {"HappyHome Quest"}, "timezone": {"America/Chicago"},
		"location_query": {"Chicago"}, "units": {"imperial"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	name, err := ts.App.Settings.Get(t.Context(), weather.SettingLocationName, "")
	if err != nil || name != "Chicago, Illinois, United States" {
		t.Fatalf("weather_location_name = %q, err=%v", name, err)
	}

	pollUntilWeatherCached(t, ts.App)
}

// TestUpdateSettingsRejectsUnresolvableLocation covers the geocode-failure
// branch, which must not save a broken/partial location.
func TestUpdateSettingsRejectsUnresolvableLocation(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "weather-settings-bad@example.com", "s3cret-password")

	geocodeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer geocodeSrv.Close()
	origGeocodeURL := weather.GeocodeURL
	weather.GeocodeURL = geocodeSrv.URL
	defer func() { weather.GeocodeURL = origGeocodeURL }()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/parent/settings", strings.NewReader(url.Values{
		"app_title": {"HappyHome Quest"}, "timezone": {"America/Chicago"},
		"location_query": {"Nowhereville"}, "csrf_token": {csrfToken},
	}.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")

	resp, err := ts.Client.Do(req)
	if err != nil {
		t.Fatalf("POST settings: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (htmx error fragment)", resp.StatusCode)
	}
	if !strings.Contains(string(body), "find that location") {
		t.Errorf("expected geocode error message in fragment, got body: %s", body)
	}

	name, err := ts.App.Settings.Get(t.Context(), weather.SettingLocationName, "")
	if err != nil || name != "" {
		t.Fatalf("expected no weather_location_name to be saved, got %q (err=%v)", name, err)
	}
}

// pollUntilWeatherCached waits for refreshWeatherAsync's background goroutine
// to populate the cache, rather than guessing with a fixed sleep.
func pollUntilWeatherCached(t *testing.T, app *handlers.App) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := app.Weather.Get(); ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for refreshWeatherAsync to populate the weather cache")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
