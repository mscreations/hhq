package main

import (
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/weather"
)

// This file targets internal/handlers/parent.go's remaining coverage gaps,
// following the same real-Postgres/real-router/broken-store conventions as
// router_test.go and the sibling *_coverage_test.go files (password reset,
// setup, invite, auth handlers, kiosk, google oauth). Only this file is
// added - no existing test file is modified.

// postFormHX is postForm's htmx-request counterpart: it sets the HX-Request
// header htmx issues on every request it sends, so handlers that branch on
// r.Header.Get("HX-Request") (see respondAfterMutation/respondParentsError/
// respondSettingsError in internal/handlers/parent.go) take their fragment-
// render path instead of the plain-form redirect path.
func (ts *testServer) postFormHX(t *testing.T, path, csrfToken string, values url.Values) *http.Response {
	t.Helper()
	if values == nil {
		values = url.Values{}
	}
	values.Set("csrf_token", csrfToken)
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatalf("building request for %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err := ts.Client.Do(req)
	if err != nil {
		t.Fatalf("POST(htmx) %s: %v", path, err)
	}
	return resp
}

// failingReader is an io.Reader that always errors - swapped in for
// crypto/rand.Reader (a package-level var, safe to override in a test
// process since these tests don't run in parallel) to deterministically
// exercise util.Encryptor.Encrypt's otherwise-unreachable nonce-generation
// error branch, which parent.go's CreateCalendarAccount/UpdateCalendarAccount
// surface as a 500 when encrypting a submitted CalDAV/refresh-token password.
type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	return 0, errors.New("forced rand failure for test")
}

// withFailingRand swaps crypto/rand.Reader for the duration of the test,
// restoring the original via t.Cleanup.
func withFailingRand(t *testing.T) {
	t.Helper()
	orig := cryptorand.Reader
	cryptorand.Reader = failingReader{}
	t.Cleanup(func() { cryptorand.Reader = orig })
}

// --- buildParentDashboardData / ParentDashboard store-error branches ---
// (respondAfterMutation reuses buildParentDashboardData too, but these tests
// go straight at ParentDashboard's GET /parent, which is the simplest way to
// exercise each of the function's many read calls in isolation.)

func TestBuildParentDashboardDataStoreErrorBranches(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "dashboard-data-errors@example.com", "s3cret-password")
	_ = csrfToken
	broken := brokenDB(t)

	cases := []struct {
		name  string
		swap  func()
		reset func()
	}{
		{
			name: "Users broken (ListChildren)",
			swap: func() { ts.App.Users = &models.UserStore{DB: broken} },
		},
		{
			name: "CalendarAccounts broken (ListAll)",
			swap: func() { ts.App.CalendarAccounts = &models.CalendarAccountStore{DB: broken} },
		},
		{
			name: "Chores broken (ListActive)",
			swap: func() { ts.App.Chores = &models.ChoreStore{DB: broken} },
		},
		{
			name: "ChoreDefs broken (ListActive)",
			swap: func() { ts.App.ChoreDefs = &models.ChoreDefinitionStore{DB: broken} },
		},
		{
			name: "Plugins broken (ListAll)",
			swap: func() { ts.App.Plugins = &models.PluginStore{DB: broken} },
		},
		{
			name: "ChoreInstances broken (ListForWeek)",
			swap: func() { ts.App.ChoreInstances = &models.ChoreInstanceStore{DB: broken} },
		},
	}

	origUsers := ts.App.Users
	origCalendarAccounts := ts.App.CalendarAccounts
	origChores := ts.App.Chores
	origChoreDefs := ts.App.ChoreDefs
	origPlugins := ts.App.Plugins
	origChoreInstances := ts.App.ChoreInstances
	resetAll := func() {
		ts.App.Users = origUsers
		ts.App.CalendarAccounts = origCalendarAccounts
		ts.App.Chores = origChores
		ts.App.ChoreDefs = origChoreDefs
		ts.App.Plugins = origPlugins
		ts.App.ChoreInstances = origChoreInstances
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.swap()
			defer resetAll()

			resp, err := ts.Client.Get(ts.URL + "/parent")
			if err != nil {
				t.Fatalf("GET /parent: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusInternalServerError {
				t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
			}
		})
	}
}

// TestBuildParentDashboardDataListForAccountErrorBranch covers the
// Calendars.ListForAccount error branch specifically - it only runs inside
// the per-account loop, so at least one calendar account must exist first
// (created with a working store) before Calendars is broken.
func TestBuildParentDashboardDataListForAccountErrorBranch(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "dashboard-data-calendars-error@example.com", "s3cret-password")

	if _, err := ts.App.CalendarAccounts.Create(t.Context(), models.CalendarAccount{
		Name: "Acct", Provider: models.ProviderGeneric, EncryptedPassword: []byte("x"),
	}); err != nil {
		t.Fatalf("CalendarAccounts.Create: %v", err)
	}

	ts.App.Calendars = &models.CalendarStore{DB: brokenDB(t)}

	resp, err := ts.Client.Get(ts.URL + "/parent")
	if err != nil {
		t.Fatalf("GET /parent: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestBuildParentDashboardDataFullHappyPath exercises buildParentDashboardData
// with real, populated data across the board in one pass: a calendar account
// with a discovered calendar (covers the account/calendar loop's success
// path and groupChoreDefsByChild's non-empty branch by way of a real chore
// definition), and a pending chore instance (covers the pending-approvals
// filter's true branch).
func TestBuildParentDashboardDataFullHappyPath(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "dashboard-data-happy@example.com", "s3cret-password")
	ctx := t.Context()

	accountID, err := ts.App.CalendarAccounts.Create(ctx, models.CalendarAccount{
		Name: "Acct", Provider: models.ProviderGeneric, EncryptedPassword: []byte("x"),
	})
	if err != nil {
		t.Fatalf("CalendarAccounts.Create: %v", err)
	}
	if _, err := ts.App.Calendars.UpsertDiscovered(ctx, accountID, "/path/", "Home", "#3B82F6"); err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}

	childID, err := ts.App.Users.CreateChild(ctx, "Kid", "#EF4444")
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
	if _, err := ts.App.ChoreInstances.MarkComplete(ctx, instances[0].ID); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}

	resp, err := ts.Client.Get(ts.URL + "/parent")
	if err != nil {
		t.Fatalf("GET /parent: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "Dishes") {
		t.Errorf("expected the dashboard to render the chore definition, body = %s", body)
	}
}

// --- respondAfterMutation / respondParentsError / respondSettingsError build-error branches ---

// TestRespondAfterMutationReturnsServerErrorWhenRebuildFails covers
// respondAfterMutation's htmx path when the post-mutation rebuild
// (buildParentDashboardData) itself fails. CreateChild only touches the
// Users store, so breaking Plugins (used only by the rebuild) isolates this
// from the mutation itself succeeding.
func TestRespondAfterMutationReturnsServerErrorWhenRebuildFails(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "respond-after-mutation-error@example.com", "s3cret-password")
	ts.App.Plugins = &models.PluginStore{DB: brokenDB(t)}

	resp := ts.postFormHX(t, "/parent/users/children", csrfToken, url.Values{"name": {"Kid"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestRespondParentsErrorReturnsServerErrorWhenRebuildFails covers
// respondParentsError's htmx path when the rebuild fails, triggered via the
// duplicate-invite-email error path (the other respondParentsError caller,
// "can't remove the last parent", would work identically).
func TestRespondParentsErrorReturnsServerErrorWhenRebuildFails(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "respond-parents-error@example.com", "s3cret-password")
	ts.App.Chores = &models.ChoreStore{DB: brokenDB(t)}

	resp := ts.postFormHX(t, "/parent/users/parents", csrfToken, url.Values{
		"name": {"Dup"}, "email": {"respond-parents-error@example.com"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestRespondSettingsErrorReturnsServerErrorWhenRebuildFails covers
// respondSettingsError's htmx path when the rebuild fails, triggered via the
// invalid-timezone validation error (which is checked before any Settings
// write, so breaking a different store - Chores - doesn't interfere with
// reaching that check).
func TestRespondSettingsErrorReturnsServerErrorWhenRebuildFails(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "respond-settings-error@example.com", "s3cret-password")
	ts.App.Chores = &models.ChoreStore{DB: brokenDB(t)}

	resp := ts.postFormHX(t, "/parent/settings", csrfToken, url.Values{
		"app_title": {"X"}, "timezone": {"Not/A/Real/Zone"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// --- UpdateSettings ---

func TestUpdateSettingsRejectsMalformedForm(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "settings-malformed@example.com", "s3cret-password")

	resp, err := ts.Client.Post(ts.URL+"/parent/settings?%zz", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /parent/settings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestUpdateSettingsBlankFieldsUseDefaults covers the blank-app_title and
// blank-timezone default-substitution branches.
func TestUpdateSettingsBlankFieldsUseDefaults(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "settings-blank-defaults@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/settings", csrfToken, url.Values{"app_title": {""}, "timezone": {""}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	title, err := ts.App.Settings.Get(t.Context(), "app_title", "")
	if err != nil || title != "HappyHome Quest" {
		t.Fatalf("app_title = %q, err=%v, want default", title, err)
	}
	timezone, err := ts.App.Settings.Get(t.Context(), "timezone", "")
	if err != nil || timezone != "America/New_York" {
		t.Fatalf("timezone = %q, err=%v, want default", timezone, err)
	}
}

// TestUpdateSettingsRejectsInvalidRadarZoom covers the radar_zoom
// validation branch (non-numeric or out-of-range values are rejected
// without touching the stored value).
func TestUpdateSettingsRejectsInvalidRadarZoom(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "settings-bad-radar-zoom@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/settings", csrfToken, url.Values{
		"app_title": {"X"}, "timezone": {"America/Chicago"}, "radar_zoom": {"not-a-number"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect with settings_error)", resp.StatusCode, http.StatusSeeOther)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "settings_error=") {
		t.Fatalf("Location = %q, want it to carry settings_error", loc)
	}
}

// TestUpdateSettingsRejectsLocationNotFound covers weather.Geocode's
// no-results error branch, by pointing weather.GeocodeURL at a local fake
// server that always returns zero results.
func TestUpdateSettingsRejectsLocationNotFound(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "settings-bad-location@example.com", "s3cret-password")

	fakeGeocoder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer fakeGeocoder.Close()
	origURL := weather.GeocodeURL
	weather.GeocodeURL = fakeGeocoder.URL
	defer func() { weather.GeocodeURL = origURL }()

	resp := ts.postForm(t, "/parent/settings", csrfToken, url.Values{
		"app_title": {"X"}, "timezone": {"America/Chicago"}, "location_query": {"Nowhereville"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect with settings_error)", resp.StatusCode, http.StatusSeeOther)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "settings_error=") {
		t.Fatalf("Location = %q, want it to carry settings_error", loc)
	}
}

// --- Users: CreateChild / CreateParent / ResendParentInvite / RemoveUser ---

func TestCreateChildRejectsMalformedForm(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "create-child-malformed@example.com", "s3cret-password")

	resp, err := ts.Client.Post(ts.URL+"/parent/users/children?%zz", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestCreateParentRejectsMalformedForm(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "create-parent-malformed@example.com", "s3cret-password")

	resp, err := ts.Client.Post(ts.URL+"/parent/users/parents?%zz", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestCreateParentReturnsServerErrorWhenInviteEmailFails covers CreateParent's
// sendInviteEmail-failure branch (and, transitively, sendInviteEmail's own
// Settings.Get error branch) by breaking Settings - InviteParent itself only
// needs the Users store, which stays intact, so the invite row is created
// successfully before the email step fails.
func TestCreateParentReturnsServerErrorWhenInviteEmailFails(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "create-parent-invite-email-fails@example.com", "s3cret-password")
	ts.App.Settings = &models.SettingsStore{DB: brokenDB(t)}

	resp := ts.postForm(t, "/parent/users/parents", csrfToken, url.Values{
		"name": {"New Parent"}, "email": {"new-invitee@example.com"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestResendParentInviteReturnsServerErrorOnGenericGetByIDFailure covers
// ResendParentInvite's non-ErrNotFound GetByID error branch.
func TestResendParentInviteReturnsServerErrorOnGenericGetByIDFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "resend-invite-getbyid-error@example.com", "s3cret-password")
	ts.App.Users = &models.UserStore{DB: brokenDB(t)}

	resp := ts.postForm(t, "/parent/users/1/resend-invite", csrfToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestResendParentInviteReturnsServerErrorWhenEmailFails covers
// ResendParentInvite's own sendInviteEmail-failure branch, isolated from the
// GetByID/MarkInvited calls (which stay on the real, working Users store) by
// breaking the mailer's connectivity instead of any store.
func TestResendParentInviteReturnsServerErrorWhenEmailFails(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "resend-invite-email-fails@example.com", "s3cret-password")

	id, err := ts.App.Users.InviteParent(t.Context(), "Pending Parent", "pending-resend@example.com")
	if err != nil {
		t.Fatalf("InviteParent: %v", err)
	}
	ts.App.Mailer.Port = 1 // nothing listens here; dialing fails immediately

	resp := ts.postForm(t, fmt.Sprintf("/parent/users/%d/resend-invite", id), csrfToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestRemoveUserReturnsServerErrorOnGenericGetByIDFailure covers RemoveUser's
// non-ErrNotFound GetByID error branch.
func TestRemoveUserReturnsServerErrorOnGenericGetByIDFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "remove-user-getbyid-error@example.com", "s3cret-password")
	ts.App.Users = &models.UserStore{DB: brokenDB(t)}

	resp := ts.postForm(t, "/parent/users/1/remove", csrfToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestRemoveUserRemovesNonLastParentSuccessfully covers RemoveUser's
// role==Parent success branch (respondAfterMutation with "parent/_parents") -
// every existing RemoveUser test only exercises removing a child.
func TestRemoveUserRemovesNonLastParentSuccessfully(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "remove-parent-success-1@example.com", "s3cret-password")

	secondID, err := ts.App.Users.CreateParent(t.Context(), "Second Parent", "remove-parent-success-2@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/users/%d/remove", secondID), csrfToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	parents, err := ts.App.Users.ListParents(t.Context())
	if err != nil {
		t.Fatalf("ListParents: %v", err)
	}
	for _, p := range parents {
		if p.ID == secondID {
			t.Fatal("expected the removed parent to no longer be listed")
		}
	}
}

// --- Calendar accounts ---

func TestCreateCalendarAccountRejectsMalformedForm(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "create-cal-malformed@example.com", "s3cret-password")

	resp, err := ts.Client.Post(ts.URL+"/parent/calendar-accounts?%zz", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestCreateCalendarAccountReturnsServerErrorOnEncryptFailure covers the
// Encryptor.Encrypt error branch, by forcing crypto/rand.Reader (used for
// AES-GCM's nonce) to fail - otherwise unreachable through normal inputs.
func TestCreateCalendarAccountReturnsServerErrorOnEncryptFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "create-cal-encrypt-fail@example.com", "s3cret-password")
	withFailingRand(t)

	resp := ts.postForm(t, "/parent/calendar-accounts", csrfToken, url.Values{
		"provider": {"caldav_generic"}, "name": {"X"}, "username": {"u"}, "password": {"p"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestEditCalendarAccountPageReturnsServerErrorOnGenericGetByIDFailure covers
// EditCalendarAccountPage's non-ErrAccountNotFound error branch.
func TestEditCalendarAccountPageReturnsServerErrorOnGenericGetByIDFailure(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "edit-cal-getbyid-error@example.com", "s3cret-password")
	ts.App.CalendarAccounts = &models.CalendarAccountStore{DB: brokenDB(t)}

	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/1/edit")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

func TestUpdateCalendarAccountNotFound(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "update-cal-notfound@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/calendar-accounts/99999", csrfToken, url.Values{"name": {"X"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestUpdateCalendarAccountReturnsServerErrorOnGenericGetByIDFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "update-cal-getbyid-error@example.com", "s3cret-password")
	ts.App.CalendarAccounts = &models.CalendarAccountStore{DB: brokenDB(t)}

	resp := ts.postForm(t, "/parent/calendar-accounts/1", csrfToken, url.Values{"name": {"X"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

func TestUpdateCalendarAccountRejectsMalformedForm(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "update-cal-malformed@example.com", "s3cret-password")

	accountID, err := ts.App.CalendarAccounts.Create(t.Context(), models.CalendarAccount{
		Name: "Acct", Provider: models.ProviderGeneric, EncryptedPassword: []byte("x"),
	})
	if err != nil {
		t.Fatalf("CalendarAccounts.Create: %v", err)
	}

	resp, err := ts.Client.Post(
		fmt.Sprintf("%s/parent/calendar-accounts/%d?%%zz&csrf_token=%s", ts.URL, accountID, csrfToken),
		"application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestUpdateCalendarAccountGoogleProviderRenamesLabelOnly covers
// UpdateCalendarAccount's Google-provider branch (UpdateGoogleName), which no
// existing test exercises - every other UpdateCalendarAccount test uses a
// CalDAV-provider account.
func TestUpdateCalendarAccountGoogleProviderRenamesLabelOnly(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "update-cal-google@example.com", "s3cret-password")

	accountID, err := ts.App.CalendarAccounts.CreateGoogle(t.Context(), "Google Acct", []byte("enc-refresh-token"), "kid@gmail.com")
	if err != nil {
		t.Fatalf("CreateGoogle: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/calendar-accounts/%d", accountID), csrfToken, url.Values{"name": {"Renamed Google"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	account, err := ts.App.CalendarAccounts.GetByID(t.Context(), accountID)
	if err != nil || account.Name != "Renamed Google" {
		t.Fatalf("GetByID: account=%+v err=%v", account, err)
	}
}

// TestUpdateCalendarAccountReturnsServerErrorOnEncryptFailure covers the
// non-blank-password Encrypt error branch, same forced-rand-failure
// technique as the CreateCalendarAccount version.
func TestUpdateCalendarAccountReturnsServerErrorOnEncryptFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "update-cal-encrypt-fail@example.com", "s3cret-password")

	accountID, err := ts.App.CalendarAccounts.Create(t.Context(), models.CalendarAccount{
		Name: "Acct", Provider: models.ProviderGeneric, EncryptedPassword: []byte("x"),
	})
	if err != nil {
		t.Fatalf("CalendarAccounts.Create: %v", err)
	}
	withFailingRand(t)

	resp := ts.postForm(t, fmt.Sprintf("/parent/calendar-accounts/%d", accountID), csrfToken, url.Values{
		"name": {"Acct"}, "caldav_url": {"https://example.com/dav/"}, "username": {"u"}, "password": {"new-password"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

func TestDeleteCalendarAccountNotFound(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "delete-cal-notfound@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/calendar-accounts/99999/delete", csrfToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestDeleteCalendarAccountReturnsServerErrorOnGenericGetByIDFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "delete-cal-getbyid-error@example.com", "s3cret-password")
	ts.App.CalendarAccounts = &models.CalendarAccountStore{DB: brokenDB(t)}

	resp := ts.postForm(t, "/parent/calendar-accounts/1/delete", csrfToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// --- RefreshCalendarAccounts (0% coverage before this file) ---

func TestRefreshCalendarAccountsRendersFragment(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "refresh-cal-accounts@example.com", "s3cret-password")

	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/refresh")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestRefreshCalendarAccountsReturnsServerErrorOnStoreFailure(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "refresh-cal-accounts-error@example.com", "s3cret-password")
	ts.App.Chores = &models.ChoreStore{DB: brokenDB(t)}

	resp, err := ts.Client.Get(ts.URL + "/parent/calendar-accounts/refresh")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// --- SetCalendarColor (0% coverage before this file) ---

func TestSetCalendarColorInvalidID(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "set-color-invalid-id@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/calendars/not-a-number/color", csrfToken, url.Values{"color": {models.PaletteColors()[0]}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestSetCalendarColorRejectsInvalidColor(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "set-color-invalid-color@example.com", "s3cret-password")
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

	resp := ts.postForm(t, fmt.Sprintf("/parent/calendars/%d/color", calID), csrfToken, url.Values{"color": {"not-a-real-color"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestSetCalendarColorSuccess(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "set-color-success@example.com", "s3cret-password")
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

	newColor := models.PaletteColors()[3]
	resp := ts.postForm(t, fmt.Sprintf("/parent/calendars/%d/color", calID), csrfToken, url.Values{"color": {newColor}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	cal, err := ts.App.Calendars.GetByID(ctx, calID)
	if err != nil || cal.Color != newColor {
		t.Fatalf("GetByID: cal=%+v err=%v, want color=%q", cal, err, newColor)
	}
}

func TestSetCalendarColorReturnsServerErrorOnStoreFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "set-color-store-error@example.com", "s3cret-password")
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
	ts.App.Calendars = &models.CalendarStore{DB: brokenDB(t)}

	resp := ts.postForm(t, fmt.Sprintf("/parent/calendars/%d/color", calID), csrfToken, url.Values{"color": {models.PaletteColors()[0]}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// --- CreateChoreDefinition ---

func TestCreateChoreDefinitionRejectsMalformedForm(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "chore-def-malformed@example.com", "s3cret-password")

	resp, err := ts.Client.Post(ts.URL+"/parent/chores/definitions?%zz", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestCreateChoreDefinitionRejectsInvalidChildID(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-def-invalid-child@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {"not-a-number"}, "chore_id": {"new"}, "new_chore_name": {"Dishes"}, "kind": {"recurring"}, "days": {"1"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestCreateChoreDefinitionDefaultsNonPositivePoints covers the
// "points <= 0 defaults to 1" branch.
func TestCreateChoreDefinitionDefaultsNonPositivePoints(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-def-zero-points@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {fmt.Sprint(childID)}, "chore_id": {"new"}, "new_chore_name": {"Dishes"},
		"points": {"0"}, "kind": {"recurring"}, "days": {"1"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	defs, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defs) != 1 || defs[0].Points != 1 {
		t.Fatalf("ListActive: defs=%+v err=%v, want one def with Points=1", defs, err)
	}
}

func TestCreateChoreDefinitionRejectsBlankNewChoreName(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-def-blank-name@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {fmt.Sprint(childID)}, "chore_id": {"new"}, "new_chore_name": {""},
		"points": {"5"}, "kind": {"recurring"}, "days": {"1"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestCreateChoreDefinitionReturnsServerErrorWhenChoresCreateFails(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-def-chores-create-fail@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	ts.App.Chores = &models.ChoreStore{DB: brokenDB(t)}

	resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {fmt.Sprint(childID)}, "chore_id": {"new"}, "new_chore_name": {"Dishes"},
		"points": {"5"}, "kind": {"recurring"}, "days": {"1"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

func TestCreateChoreDefinitionRejectsInvalidExistingChoreID(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-def-invalid-chore-id@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {fmt.Sprint(childID)}, "chore_id": {"not-a-number"},
		"points": {"5"}, "kind": {"recurring"}, "days": {"1"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestCreateChoreDefinitionWithExistingChoreID covers assigning an already-
// existing catalog chore (chore_id set to a real ID, not "new") - every
// other CreateChoreDefinition test uses "new".
func TestCreateChoreDefinitionWithExistingChoreID(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-def-existing-chore-id@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	choreID, err := ts.App.Chores.Create(t.Context(), "Existing Chore", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}

	resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {fmt.Sprint(childID)}, "chore_id": {fmt.Sprint(choreID)},
		"points": {"5"}, "kind": {"one_off"}, "one_off_date": {"2026-09-01"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
}

// TestCreateChoreDefinitionForParentAssigneeForcesZeroPoints covers assigning
// a chore to a parent instead of a child: it's informational only, so any
// submitted points value is forced to 0 server-side regardless of what the
// (JS-disabled-in-this-test) form posted.
func TestCreateChoreDefinitionForParentAssigneeForcesZeroPoints(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-def-parent-assignee@example.com", "s3cret-password")

	parents, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parents) != 1 {
		t.Fatalf("ListParents: parents=%+v err=%v", parents, err)
	}
	parentID := parents[0].ID

	resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {fmt.Sprint(parentID)}, "chore_id": {"new"}, "new_chore_name": {"Dishes"},
		"points": {"5"}, "kind": {"recurring"}, "days": {"1"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	defs, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defs) != 1 {
		t.Fatalf("ListActive: defs=%+v err=%v", defs, err)
	}
	if defs[0].Points != 0 {
		t.Fatalf("Points = %d, want 0 for a parent-assigned chore regardless of submitted value", defs[0].Points)
	}
	if defs[0].AssigneeRole != models.RoleParent {
		t.Fatalf("AssigneeRole = %q, want %q", defs[0].AssigneeRole, models.RoleParent)
	}
}

// TestCreateChoreDefinitionRejectsNonexistentChildID covers the new
// assignee-lookup added alongside parent-chore-assignment support: a
// syntactically valid but nonexistent user id should 400, not fall through
// to a raw FK-violation 500 from the INSERT.
func TestCreateChoreDefinitionRejectsNonexistentChildID(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-def-nonexistent-child@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {"999999"}, "chore_id": {"new"}, "new_chore_name": {"Dishes"}, "kind": {"recurring"}, "days": {"1"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestParentDashboardRendersParentAssignedChoreAndDisplayName is a template
// smoke test (see CLAUDE.md's Round 8/14 verification notes on html/template
// parse/execute errors not being caught by go build/vet): it drives the real
// GET /parent page with a parent-assigned chore def in place and a display
// name set, confirming _chore_defs.html's new parent-group rendering and
// _parents.html's new display-name form both execute without error and show
// the expected content.
func TestParentDashboardRendersParentAssignedChoreAndDisplayName(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "dashboard-parent-chore@example.com", "s3cret-password")

	parents, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parents) != 1 {
		t.Fatalf("ListParents: parents=%+v err=%v", parents, err)
	}
	parentID := parents[0].ID
	if err := ts.App.Users.SetDisplayName(t.Context(), parentID, "Mom"); err != nil {
		t.Fatalf("SetDisplayName: %v", err)
	}

	resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {fmt.Sprint(parentID)}, "chore_id": {"new"}, "new_chore_name": {"Dishes"},
		"points": {"5"}, "kind": {"recurring"}, "days": {"1"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create chore def: status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	page, err := ts.Client.Get(ts.URL + "/parent")
	if err != nil {
		t.Fatalf("GET /parent: %v", err)
	}
	defer page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("GET /parent status = %d, want 200", page.StatusCode)
	}
	body, err := io.ReadAll(page.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	html := string(body)
	if !strings.Contains(html, "Dishes") {
		t.Fatal("dashboard HTML missing the parent-assigned chore's name")
	}
	if !strings.Contains(html, "informational only") {
		t.Fatal("dashboard HTML missing the informational-only parent-group label")
	}
	if !strings.Contains(html, `value="Mom"`) {
		t.Fatal("dashboard HTML missing the saved display name in the Parents card form")
	}
}

// TestSetParentDisplayNameShowsSavedConfirmation covers the "no feedback
// after saving" fix: an htmx request to /parent/users/{id}/display-name
// should get back the _parents fragment with a "Saved" confirmation next to
// the row that was just updated, not just the same-looking form.
func TestSetParentDisplayNameShowsSavedConfirmation(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "display-name-saved-confirmation@example.com", "s3cret-password")

	parents, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parents) != 1 {
		t.Fatalf("ListParents: parents=%+v err=%v", parents, err)
	}
	parentID := parents[0].ID

	resp := ts.postFormHX(t, fmt.Sprintf("/parent/users/%d/display-name", parentID), csrfToken, url.Values{
		"display_name": {"Dad"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	html := string(body)
	if !strings.Contains(html, "Saved") {
		t.Fatal("response missing the \"Saved\" confirmation after setting a display name")
	}
	if !strings.Contains(html, `value="Dad"`) {
		t.Fatal("response missing the newly-saved display name value")
	}
}

// TestSetParentDisplayNameNonHXRedirectsWithSavedQueryParam covers the
// plain-form (no htmx) fallback path: it should redirect back to /parent
// with a query param that makes the next GET show the same confirmation.
func TestSetParentDisplayNameNonHXRedirectsWithSavedQueryParam(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "display-name-saved-redirect@example.com", "s3cret-password")

	parents, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parents) != 1 {
		t.Fatalf("ListParents: parents=%+v err=%v", parents, err)
	}
	parentID := parents[0].ID

	resp := ts.postForm(t, fmt.Sprintf("/parent/users/%d/display-name", parentID), csrfToken, url.Values{
		"display_name": {"Dad"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	wantLoc := fmt.Sprintf("/parent?display_name_saved=%d", parentID)
	if loc := resp.Header.Get("Location"); loc != wantLoc {
		t.Fatalf("Location = %q, want %q", loc, wantLoc)
	}

	page, err := ts.Client.Get(ts.URL + wantLoc)
	if err != nil {
		t.Fatalf("GET %s: %v", wantLoc, err)
	}
	defer page.Body.Close()
	body, err := io.ReadAll(page.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !strings.Contains(string(body), "Saved") {
		t.Fatal("dashboard HTML missing the \"Saved\" confirmation after following the redirect")
	}
}

func TestCreateChoreDefinitionOneOffRejectsInvalidDate(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-def-invalid-date@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {fmt.Sprint(childID)}, "chore_id": {"new"}, "new_chore_name": {"Wash Car"},
		"points": {"5"}, "kind": {"one_off"}, "one_off_date": {"not-a-date"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestCreateChoreDefinitionOneOffReturnsServerErrorOnStoreFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-def-oneoff-store-error@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	ts.App.ChoreDefs = &models.ChoreDefinitionStore{DB: brokenDB(t)}

	resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {fmt.Sprint(childID)}, "chore_id": {"new"}, "new_chore_name": {"Wash Car"},
		"points": {"5"}, "kind": {"one_off"}, "one_off_date": {"2026-09-01"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestCreateChoreDefinitionRecurringSkipsNonNumericDay covers the days-loop's
// strconv-error "continue" branch by mixing a non-numeric value in with a
// valid one.
func TestCreateChoreDefinitionRecurringSkipsNonNumericDay(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "chore-def-bad-day@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	resp := ts.postForm(t, "/parent/chores/definitions", csrfToken, url.Values{
		"child_id": {fmt.Sprint(childID)}, "chore_id": {"new"}, "new_chore_name": {"Vacuum"},
		"points": {"5"}, "kind": {"recurring"}, "days": {"not-a-day", "2"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
}

// --- DeactivateChoreDefinition / checkChoreDefinitionNotBootstrapManaged ---

// TestDeactivateChoreDefinitionReturnsServerErrorWhenListActiveFails covers
// checkChoreDefinitionNotBootstrapManaged's ListActive-error branch.
func TestDeactivateChoreDefinitionReturnsServerErrorWhenListActiveFails(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "deactivate-def-listactive-error@example.com", "s3cret-password")
	ts.App.ChoreDefs = &models.ChoreDefinitionStore{DB: brokenDB(t)}

	resp := ts.postForm(t, "/parent/chores/definitions/1/deactivate", csrfToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// --- UpdateChore ---

func TestUpdateChoreInvalidID(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "update-chore-invalid-id@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/chores/catalog/not-a-number", csrfToken, url.Values{"name": {"X"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestUpdateChoreNotFound(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "update-chore-notfound@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/chores/catalog/99999", csrfToken, url.Values{"name": {"X"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestUpdateChoreReturnsServerErrorOnGenericGetByIDFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "update-chore-getbyid-error@example.com", "s3cret-password")
	ts.App.Chores = &models.ChoreStore{DB: brokenDB(t)}

	resp := ts.postForm(t, "/parent/chores/catalog/1", csrfToken, url.Values{"name": {"X"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

func TestUpdateChoreRejectsMalformedForm(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "update-chore-malformed@example.com", "s3cret-password")

	choreID, err := ts.App.Chores.Create(t.Context(), "Dishes", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}

	resp, err := ts.Client.Post(
		fmt.Sprintf("%s/parent/chores/catalog/%d?%%zz&csrf_token=%s", ts.URL, choreID, csrfToken),
		"application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestUpdateChoreRejectsBlankName(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "update-chore-blank-name@example.com", "s3cret-password")

	choreID, err := ts.App.Chores.Create(t.Context(), "Dishes", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/chores/catalog/%d", choreID), csrfToken, url.Values{"name": {""}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestUpdateChoreSuccess(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "update-chore-success@example.com", "s3cret-password")

	choreID, err := ts.App.Chores.Create(t.Context(), "Dishes", "orig")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/chores/catalog/%d", choreID), csrfToken, url.Values{
		"name": {"Dishes Renamed"}, "description": {"new description"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	chore, err := ts.App.Chores.GetByID(t.Context(), choreID)
	if err != nil || chore.Name != "Dishes Renamed" {
		t.Fatalf("GetByID: chore=%+v err=%v", chore, err)
	}
}

// --- DeactivateChore ---

func TestDeactivateChoreInvalidID(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "deactivate-chore-invalid-id@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/chores/catalog/not-a-number/deactivate", csrfToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestDeactivateChoreNotFound(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "deactivate-chore-notfound@example.com", "s3cret-password")

	resp := ts.postForm(t, "/parent/chores/catalog/99999/deactivate", csrfToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestDeactivateChoreReturnsServerErrorOnGenericGetByIDFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "deactivate-chore-getbyid-error@example.com", "s3cret-password")
	ts.App.Chores = &models.ChoreStore{DB: brokenDB(t)}

	resp := ts.postForm(t, "/parent/chores/catalog/1/deactivate", csrfToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

func TestDeactivateChoreSuccess(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "deactivate-chore-success@example.com", "s3cret-password")

	choreID, err := ts.App.Chores.Create(t.Context(), "Dishes", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}

	resp := ts.postForm(t, fmt.Sprintf("/parent/chores/catalog/%d/deactivate", choreID), csrfToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	chores, err := ts.App.Chores.ListActive(t.Context())
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	for _, c := range chores {
		if c.ID == choreID {
			t.Fatal("expected the deactivated chore to no longer be listed as active")
		}
	}
}

// --- ParentDecideChore / ParentResetRejectedChore ---

func TestParentDecideChoreRejectsMalformedForm(t *testing.T) {
	ts := newTestServer(t)
	ts.login(t, "decide-chore-malformed@example.com", "s3cret-password")

	resp, err := ts.Client.Post(ts.URL+"/parent/chores/1/decide?%zz", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestParentDecideChoreReturnsServerErrorOnGenericStoreFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "decide-chore-store-error@example.com", "s3cret-password")
	ts.App.ChoreInstances = &models.ChoreInstanceStore{DB: brokenDB(t)}

	resp := ts.postForm(t, "/parent/chores/1/decide", csrfToken, url.Values{"decision": {"approve"}})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

func TestParentResetRejectedChoreReturnsServerErrorOnGenericStoreFailure(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "reset-rejected-store-error@example.com", "s3cret-password")
	ts.App.ChoreInstances = &models.ChoreInstanceStore{DB: brokenDB(t)}

	resp := ts.postForm(t, "/parent/chores/1/reset", csrfToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}
