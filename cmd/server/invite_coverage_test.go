package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/models"
)

// TestInviteAcceptRejectsTokenForDeletedUser covers loadPendingInvite's
// GetByID-returns-ErrNotFound branch: a cryptographically valid invite
// token whose referenced user id no longer exists.
func TestInviteAcceptRejectsTokenForDeletedUser(t *testing.T) {
	ts := newTestServer(t)
	token := ts.App.Invite.Sign(999999, auth.ActionInviteAccept, ts.App.Cfg.InviteLinkTTL)

	resp, err := ts.Client.Get(ts.URL + "/invite/accept?token=" + token)
	if err != nil {
		t.Fatalf("GET /invite/accept: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (invalid-invite page, not an error)", resp.StatusCode, http.StatusOK)
	}
}

func inviteParentToken(t *testing.T, ts *testServer, email string) (string, int) {
	t.Helper()
	id, err := ts.App.Users.InviteParent(t.Context(), "Invited Parent", email)
	if err != nil {
		t.Fatalf("InviteParent: %v", err)
	}
	return ts.App.Invite.Sign(id, auth.ActionInviteAccept, ts.App.Cfg.InviteLinkTTL), id
}

// TestInviteAcceptSubmitRejectsEmptyPassword covers InviteAcceptSubmit's
// password-required validation branch.
func TestInviteAcceptSubmitRejectsEmptyPassword(t *testing.T) {
	ts := newTestServer(t)
	token, _ := inviteParentToken(t, ts, "empty-pw-invite@example.com")

	resp, err := ts.Client.PostForm(ts.URL+"/invite/accept", url.Values{
		"token": {token}, "password": {""}, "confirm_password": {""},
	})
	if err != nil {
		t.Fatalf("POST /invite/accept: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "Password is required") {
		t.Errorf("expected a password-required message, got: %s", body)
	}
}

// TestInviteAcceptSubmitRejectsOverlongPassword covers InviteAcceptSubmit's
// HashPassword-error branch.
func TestInviteAcceptSubmitRejectsOverlongPassword(t *testing.T) {
	ts := newTestServer(t)
	token, _ := inviteParentToken(t, ts, "overlong-invite@example.com")
	overlong := strings.Repeat("a", 100)

	resp, err := ts.Client.PostForm(ts.URL+"/invite/accept", url.Values{
		"token": {token}, "password": {overlong}, "confirm_password": {overlong},
	})
	if err != nil {
		t.Fatalf("POST /invite/accept: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestInviteAcceptSubmitReturnsServerErrorOnLoginFailure covers
// InviteAcceptSubmit's SessionMgr.Login error branch, isolated by breaking
// only the SessionManager's own captured Sessions store (Users, used by
// SetPasswordAndAccept just before, stays intact).
func TestInviteAcceptSubmitReturnsServerErrorOnLoginFailure(t *testing.T) {
	ts := newTestServer(t)
	token, _ := inviteParentToken(t, ts, "login-fail-invite@example.com")
	ts.App.SessionMgr.Sessions = &models.SessionStore{DB: brokenDB(t)}

	resp, err := ts.Client.PostForm(ts.URL+"/invite/accept", url.Values{
		"token": {token}, "password": {"hunter22"}, "confirm_password": {"hunter22"},
	})
	if err != nil {
		t.Fatalf("POST /invite/accept: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}
