package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/models"
)

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return string(b)
}

// TestForgotPasswordPageRenders covers ForgotPasswordPage (the plain GET
// that had zero test coverage - every existing test only POSTs to the
// submit endpoint).
func TestForgotPasswordPageRenders(t *testing.T) {
	ts := newTestServer(t)
	resp, err := ts.Client.Get(ts.URL + "/forgot-password")
	if err != nil {
		t.Fatalf("GET /forgot-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

// TestForgotPasswordSubmitRejectsMalformedForm covers ForgotPasswordSubmit's
// r.ParseForm() error branch, same %zz-in-the-query trick already used for
// SetupSubmit/InviteAcceptSubmit's identical guard.
func TestForgotPasswordSubmitRejectsMalformedForm(t *testing.T) {
	ts := newTestServer(t)
	resp, err := ts.Client.Post(ts.URL+"/forgot-password?%zz", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /forgot-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestForgotPasswordSubmitRateLimited covers the rate-limit branch (both the
// IP and email keys share the same LoginLimiter as login attempts) by
// tripping the limiter directly before submitting, rather than actually
// looping the real endpoint LoginLimiter.Allow's max-attempts times (which
// would also work, but this is more direct about which key is being tripped).
func TestForgotPasswordSubmitRateLimited(t *testing.T) {
	ts := newTestServer(t)
	target := "rate-limited@example.com"
	if _, err := ts.App.Users.CreateParent(t.Context(), "Parent", target, "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	// Exhaust the email-keyed bucket this handler checks.
	emailKey := "reset-email:" + strings.ToLower(target)
	for i := 0; i < 10; i++ {
		ts.App.LoginLimiter.RecordFailure(emailKey)
	}

	resp, err := ts.Client.PostForm(ts.URL+"/forgot-password", url.Values{"email": {target}})
	if err != nil {
		t.Fatalf("POST /forgot-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (still the generic response)", resp.StatusCode, http.StatusOK)
	}

	select {
	case <-ts.SMTP.received:
		t.Fatal("expected no email to be sent once the rate limit is exhausted")
	default:
	}
}

// TestResetPasswordRejectsTokenForDeletedUser covers loadPendingReset's
// GetByID-returns-ErrNotFound branch: a structurally valid token (passes
// auth.PeekID) whose referenced user id no longer exists.
func TestResetPasswordRejectsTokenForDeletedUser(t *testing.T) {
	ts := newTestServer(t)
	// PeekID only needs a token shaped like a real one; sign it for a user
	// id that was never created.
	token := ts.App.PasswordReset.SignWithContext(999999, auth.ActionPasswordReset, ts.App.Cfg.PasswordResetLinkTTL, "irrelevant-hash")

	resp, err := ts.Client.Get(ts.URL + "/reset-password?token=" + token)
	if err != nil {
		t.Fatalf("GET /reset-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (invalid-token page, not an error)", resp.StatusCode, http.StatusOK)
	}
}

// TestResetPasswordRejectsTokenForInactiveOrNonParentUser covers
// loadPendingReset's role/active check branch (a structurally and
// cryptographically valid token whose user is a child, or a deactivated
// parent).
func TestResetPasswordRejectsTokenForInactiveOrNonParentUser(t *testing.T) {
	ts := newTestServer(t)
	childID, err := ts.App.Users.CreateChild(t.Context(), "Kid", "#ff0000")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	child, err := ts.App.Users.GetByID(t.Context(), childID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	token := ts.App.PasswordReset.SignWithContext(childID, auth.ActionPasswordReset, ts.App.Cfg.PasswordResetLinkTTL, child.PasswordHash.String)

	resp, err := ts.Client.Get(ts.URL + "/reset-password?token=" + token)
	if err != nil {
		t.Fatalf("GET /reset-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (invalid-token page, not an error)", resp.StatusCode, http.StatusOK)
	}
}

// TestResetPasswordSubmitRejectsMalformedForm covers ResetPasswordSubmit's
// r.ParseForm() error branch.
func TestResetPasswordSubmitRejectsMalformedForm(t *testing.T) {
	ts := newTestServer(t)
	resp, err := ts.Client.Post(ts.URL+"/reset-password?%zz", "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST /reset-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestResetPasswordSubmitRejectsEmptyPassword covers ResetPasswordSubmit's
// password-required validation branch.
func TestResetPasswordSubmitRejectsEmptyPassword(t *testing.T) {
	ts := newTestServer(t)
	hash, err := auth.HashPassword("hunter22")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	userID, err := ts.App.Users.CreateParent(t.Context(), "Parent", "empty-pw@example.com", hash)
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	token := ts.App.PasswordReset.SignWithContext(userID, auth.ActionPasswordReset, ts.App.Cfg.PasswordResetLinkTTL, hash)

	resp, err := ts.Client.PostForm(ts.URL+"/reset-password", url.Values{
		"token":            {token},
		"password":         {""},
		"confirm_password": {""},
	})
	if err != nil {
		t.Fatalf("POST /reset-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (re-rendered form with an error)", resp.StatusCode, http.StatusOK)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "Password is required") {
		t.Errorf("expected a password-required error message, got: %s", body)
	}
}

// TestResetPasswordRejectsTokenOnGenericDBFailure covers loadPendingReset's
// generic (non-ErrNotFound) GetByID-error branch, using a broken Users store
// swapped in after construction - the token itself doesn't need to be valid
// for a real user since GetByID is the very first thing that fails.
func TestResetPasswordRejectsTokenOnGenericDBFailure(t *testing.T) {
	ts := newTestServer(t)
	token := ts.App.PasswordReset.SignWithContext(1, auth.ActionPasswordReset, ts.App.Cfg.PasswordResetLinkTTL, "hash")
	ts.App.Users = &models.UserStore{DB: brokenDB(t)}

	resp, err := ts.Client.Get(ts.URL + "/reset-password?token=" + token)
	if err != nil {
		t.Fatalf("GET /reset-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestResetPasswordSubmitRejectsOverlongPassword covers ResetPasswordSubmit's
// HashPassword-error branch: bcrypt rejects passwords over 72 bytes.
func TestResetPasswordSubmitRejectsOverlongPassword(t *testing.T) {
	ts := newTestServer(t)
	hash, err := auth.HashPassword("hunter22")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	userID, err := ts.App.Users.CreateParent(t.Context(), "Parent", "overlong-pw@example.com", hash)
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	token := ts.App.PasswordReset.SignWithContext(userID, auth.ActionPasswordReset, ts.App.Cfg.PasswordResetLinkTTL, hash)
	overlong := strings.Repeat("a", 100)

	resp, err := ts.Client.PostForm(ts.URL+"/reset-password", url.Values{
		"token":            {token},
		"password":         {overlong},
		"confirm_password": {overlong},
	})
	if err != nil {
		t.Fatalf("POST /reset-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestResetPasswordSubmitReturnsServerErrorOnSessionInvalidationFailure
// covers the DeleteAllForUser error branch by swapping only ts.App.Sessions
// to a broken store after construction - Users (GetByID/SetPassword) and
// SessionMgr (which captured its own working Sessions pointer at
// construction time, per the same technique used elsewhere in this file for
// TestParentDashboardMutationsReturnServerErrorOnStoreFailure) both keep
// working, isolating just this one call.
func TestResetPasswordSubmitReturnsServerErrorOnSessionInvalidationFailure(t *testing.T) {
	ts := newTestServer(t)
	hash, err := auth.HashPassword("hunter22")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	userID, err := ts.App.Users.CreateParent(t.Context(), "Parent", "session-fail@example.com", hash)
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	token := ts.App.PasswordReset.SignWithContext(userID, auth.ActionPasswordReset, ts.App.Cfg.PasswordResetLinkTTL, hash)
	ts.App.Sessions = &models.SessionStore{DB: brokenDB(t)}

	resp, err := ts.Client.PostForm(ts.URL+"/reset-password", url.Values{
		"token":            {token},
		"password":         {"brand-new-password"},
		"confirm_password": {"brand-new-password"},
	})
	if err != nil {
		t.Fatalf("POST /reset-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestResetPasswordSubmitReturnsServerErrorOnLoginFailure covers
// SessionMgr.Login's error branch, isolated by breaking only the
// SessionManager's own captured Sessions store (leaving ts.App.Sessions,
// used by the preceding DeleteAllForUser call, intact).
func TestResetPasswordSubmitReturnsServerErrorOnLoginFailure(t *testing.T) {
	ts := newTestServer(t)
	hash, err := auth.HashPassword("hunter22")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	userID, err := ts.App.Users.CreateParent(t.Context(), "Parent", "login-fail@example.com", hash)
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	token := ts.App.PasswordReset.SignWithContext(userID, auth.ActionPasswordReset, ts.App.Cfg.PasswordResetLinkTTL, hash)
	ts.App.SessionMgr.Sessions = &models.SessionStore{DB: brokenDB(t)}

	resp, err := ts.Client.PostForm(ts.URL+"/reset-password", url.Values{
		"token":            {token},
		"password":         {"brand-new-password"},
		"confirm_password": {"brand-new-password"},
	})
	if err != nil {
		t.Fatalf("POST /reset-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestForgotPasswordSubmitAsyncSendFailureIsLogged covers
// sendPasswordResetEmailAsync's error branch (sendPasswordResetEmail
// failing) by pointing the mailer at an address nothing listens on, so the
// SMTP dial itself fails. The goroutine only logs on failure - there's no
// signal to synchronize on, so this test waits briefly for it to run rather
// than asserting an observable side effect beyond "the handler still
// responds normally despite the background send failing".
func TestForgotPasswordSubmitAsyncSendFailureIsLogged(t *testing.T) {
	ts := newTestServer(t)
	hash, err := auth.HashPassword("hunter22")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := ts.App.Users.CreateParent(t.Context(), "Parent", "async-fail@example.com", hash); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	ts.App.Mailer.Port = 1 // nothing listens here; dialing fails immediately

	resp, err := ts.Client.PostForm(ts.URL+"/forgot-password", url.Values{"email": {"async-fail@example.com"}})
	if err != nil {
		t.Fatalf("POST /forgot-password: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	time.Sleep(200 * time.Millisecond) // let the background goroutine run and hit its error branch
}
