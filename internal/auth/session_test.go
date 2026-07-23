package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
)

func newTestSessionManager(t *testing.T) *SessionManager {
	t.Helper()
	conn := testutil.RequireDB(t)
	return &SessionManager{
		Sessions: &models.SessionStore{DB: conn},
		Users:    &models.UserStore{DB: conn},
		TTL:      time.Hour,
	}
}

func TestSessionManagerLoginSetsCookieAndLoadUserAttachesUser(t *testing.T) {
	sm := newTestSessionManager(t)

	userID, err := sm.Users.CreateParent(t.Context(), "Parent One", "parent@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	if err := sm.Login(rec, req, userID); err != nil {
		t.Fatalf("Login: %v", err)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != cookieName {
		t.Fatalf("expected a single %q cookie, got %+v", cookieName, cookies)
	}

	var attachedUser *models.User
	handler := sm.LoadUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attachedUser = UserFromContext(r.Context())
	}))

	req2 := httptest.NewRequest(http.MethodGet, "/parent", nil)
	req2.AddCookie(cookies[0])
	handler.ServeHTTP(httptest.NewRecorder(), req2)

	if attachedUser == nil {
		t.Fatal("expected LoadUser to attach a user to the request context")
	}
	if attachedUser.ID != userID {
		t.Fatalf("attached user ID = %d, want %d", attachedUser.ID, userID)
	}
}

func TestSessionManagerLoadUserWithNoCookieAttachesNothing(t *testing.T) {
	sm := newTestSessionManager(t)

	var attachedUser *models.User
	handler := sm.LoadUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attachedUser = UserFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/parent", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if attachedUser != nil {
		t.Fatal("expected no user attached when there is no session cookie")
	}
}

func TestSessionManagerLoadUserWithInvalidCookieAttachesNothing(t *testing.T) {
	sm := newTestSessionManager(t)

	var attachedUser *models.User
	handler := sm.LoadUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attachedUser = UserFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/parent", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "does-not-exist"})
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if attachedUser != nil {
		t.Fatal("expected no user attached for an unknown session token")
	}
}

func TestSessionManagerLogoutDeletesSessionAndClearsCookie(t *testing.T) {
	sm := newTestSessionManager(t)

	userID, err := sm.Users.CreateParent(t.Context(), "Parent Two", "parent2@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	rec := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/login", nil)
	if err := sm.Login(rec, loginReq, userID); err != nil {
		t.Fatalf("Login: %v", err)
	}
	sessionCookie := rec.Result().Cookies()[0]

	logoutRec := httptest.NewRecorder()
	logoutReq := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutReq.AddCookie(sessionCookie)
	sm.Logout(logoutRec, logoutReq)

	cleared := logoutRec.Result().Cookies()[0]
	if cleared.MaxAge >= 0 {
		t.Fatalf("expected logout cookie MaxAge < 0, got %d", cleared.MaxAge)
	}

	if _, err := sm.Sessions.Get(t.Context(), sessionCookie.Value); err != models.ErrNotFound {
		t.Fatalf("expected session to be deleted after logout, got err=%v", err)
	}
}

func TestSessionManagerRequireParentRedirectsAnonymousToLogin(t *testing.T) {
	sm := newTestSessionManager(t)

	// A parent already exists, so an anonymous visitor should go to /login, not /setup.
	if _, err := sm.Users.CreateParent(t.Context(), "Existing Parent", "existing@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	called := false
	handler := sm.RequireParent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/parent", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if called {
		t.Fatal("expected RequireParent to block an anonymous request")
	}
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?next=/parent" {
		t.Fatalf("Location = %q, want %q", loc, "/login?next=/parent")
	}
}

func TestSessionManagerRequireParentRedirectsToSetupWhenNoParentExists(t *testing.T) {
	sm := newTestSessionManager(t)

	handler := sm.RequireParent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/parent", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if loc := rec.Header().Get("Location"); loc != "/setup" {
		t.Fatalf("Location = %q, want %q", loc, "/setup")
	}
}

func TestSessionManagerRequireParentAllowsAuthenticatedParent(t *testing.T) {
	sm := newTestSessionManager(t)

	userID, err := sm.Users.CreateParent(t.Context(), "Parent Three", "parent3@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/login", nil)
	if err := sm.Login(loginRec, loginReq, userID); err != nil {
		t.Fatalf("Login: %v", err)
	}
	sessionCookie := loginRec.Result().Cookies()[0]

	called := false
	handler := sm.LoadUser(sm.RequireParent(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})))

	req := httptest.NewRequest(http.MethodGet, "/parent", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !called {
		t.Fatal("expected RequireParent to allow an authenticated parent through")
	}
}

// TestSessionManagerLoginReturnsErrorWhenSessionCreateFails exercises
// Login's error path: Sessions.Create fails (here, a foreign-key violation
// from a userID with no matching hhq_users row) and Login must propagate
// that error rather than setting a cookie for a session that was never
// actually created.
func TestSessionManagerLoginReturnsErrorWhenSessionCreateFails(t *testing.T) {
	sm := newTestSessionManager(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	err := sm.Login(rec, req, 999999)
	if err == nil {
		t.Fatal("expected Login to return an error when session creation fails")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("expected no cookie to be set when session creation fails")
	}
}

// TestSessionManagerLoadUserWithSessionButDeletedUserAttachesNothing
// exercises LoadUser's Users.GetByID failure branch: a session row is
// valid and resolves via Sessions.Get, but the user it points at no longer
// exists. Normally the sessions table's ON DELETE CASCADE means deleting a
// user also deletes their sessions, so triggers are disabled around the
// delete here to leave a dangling session behind and reach this branch.
func TestSessionManagerLoadUserWithSessionButDeletedUserAttachesNothing(t *testing.T) {
	sm := newTestSessionManager(t)
	conn := testutil.RequireDB(t)

	userID, err := sm.Users.CreateParent(t.Context(), "Parent Four", "parent4@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	if err := sm.Login(rec, req, userID); err != nil {
		t.Fatalf("Login: %v", err)
	}
	sessionCookie := rec.Result().Cookies()[0]

	// The ON DELETE CASCADE action is implemented as a trigger on the
	// referenced table (hhq_users), not the referencing table - it fires
	// when a row is deleted *from* hhq_users and deletes the matching
	// hhq_sessions rows itself. So it's hhq_users' triggers that must be
	// disabled to leave the session row behind as a dangling reference.
	if _, err := conn.Exec("ALTER TABLE hhq_users DISABLE TRIGGER ALL"); err != nil {
		t.Fatalf("disabling triggers: %v", err)
	}
	_, err = conn.Exec("DELETE FROM hhq_users WHERE id = $1", userID)
	if _, reErr := conn.Exec("ALTER TABLE hhq_users ENABLE TRIGGER ALL"); reErr != nil {
		t.Fatalf("re-enabling triggers: %v", reErr)
	}
	if err != nil {
		t.Fatalf("deleting user: %v", err)
	}

	var attachedUser *models.User
	handler := sm.LoadUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attachedUser = UserFromContext(r.Context())
	}))

	req2 := httptest.NewRequest(http.MethodGet, "/parent", nil)
	req2.AddCookie(sessionCookie)
	handler.ServeHTTP(httptest.NewRecorder(), req2)

	if attachedUser != nil {
		t.Fatal("expected no user attached when the session's user no longer exists")
	}
}

func TestSessionManagerSessionToken(t *testing.T) {
	sm := &SessionManager{}

	req := httptest.NewRequest(http.MethodGet, "/parent", nil)
	if _, ok := sm.SessionToken(req); ok {
		t.Fatal("expected no session token with no cookie")
	}

	req.AddCookie(&http.Cookie{Name: cookieName, Value: "abc123"})
	token, ok := sm.SessionToken(req)
	if !ok || token != "abc123" {
		t.Fatalf("SessionToken() = (%q, %v), want (%q, true)", token, ok, "abc123")
	}
}
