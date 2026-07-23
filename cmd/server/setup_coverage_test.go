package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mscreations/hhq/internal/models"
)

// TestSetupPageReturnsServerErrorOnListParentsFailure covers SetupPage's
// generic ListParents-error branch.
func TestSetupPageReturnsServerErrorOnListParentsFailure(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Users = &models.UserStore{DB: brokenDB(t)}

	resp, err := ts.Client.Get(ts.URL + "/setup")
	if err != nil {
		t.Fatalf("GET /setup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestSetupSubmitReturnsServerErrorOnListParentsFailure covers
// SetupSubmit's own (separate call site) generic ListParents-error branch.
func TestSetupSubmitReturnsServerErrorOnListParentsFailure(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Users = &models.UserStore{DB: brokenDB(t)}

	resp, err := ts.Client.PostForm(ts.URL+"/setup", url.Values{
		"name": {"Parent"}, "email": {"p@example.com"}, "password": {"hunter22"}, "confirm_password": {"hunter22"},
	})
	if err != nil {
		t.Fatalf("POST /setup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestSetupSubmitRejectsWhenAlreadyCompleted covers SetupSubmit's
// already-completed race-guard branch (a parent already exists by the time
// this particular request's ListParents check runs).
func TestSetupSubmitRejectsWhenAlreadyCompleted(t *testing.T) {
	ts := newTestServer(t)
	if _, err := ts.App.Users.CreateParent(t.Context(), "Existing", "existing@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	resp, err := ts.Client.PostForm(ts.URL+"/setup", url.Values{
		"name": {"New Parent"}, "email": {"new@example.com"}, "password": {"hunter22"}, "confirm_password": {"hunter22"},
	})
	if err != nil {
		t.Fatalf("POST /setup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "Setup has already been completed") {
		t.Errorf("expected the already-completed message, got: %s", body)
	}
}

// TestSetupSubmitRejectsMissingFields covers SetupSubmit's required-fields
// validation branch.
func TestSetupSubmitRejectsMissingFields(t *testing.T) {
	ts := newTestServer(t)
	resp, err := ts.Client.PostForm(ts.URL+"/setup", url.Values{
		"name": {""}, "email": {""}, "password": {""}, "confirm_password": {""},
	})
	if err != nil {
		t.Fatalf("POST /setup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "All fields are required") {
		t.Errorf("expected the required-fields message, got: %s", body)
	}
}

// TestSetupSubmitRejectsOverlongPassword covers SetupSubmit's HashPassword
// error branch (bcrypt rejects passwords over 72 bytes).
func TestSetupSubmitRejectsOverlongPassword(t *testing.T) {
	ts := newTestServer(t)
	overlong := strings.Repeat("a", 100)
	resp, err := ts.Client.PostForm(ts.URL+"/setup", url.Values{
		"name": {"Parent"}, "email": {"overlong@example.com"}, "password": {overlong}, "confirm_password": {overlong},
	})
	if err != nil {
		t.Fatalf("POST /setup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// NOTE: SetupSubmit's CreateParent-error branch is not separately covered.
// ListParents and CreateParent are both calls on the same a.Users store, so
// a broken-DB test can't isolate CreateParent's failure without also
// failing the earlier ListParents check (which would exercise that branch
// instead - see TestSetupSubmitReturnsServerErrorOnListParentsFailure
// above). Same class of same-store limitation as ResetPasswordSubmit's
// SetPassword-error branch.

// TestSetupSubmitReturnsServerErrorOnLoginFailure covers SetupSubmit's
// SessionMgr.Login error branch, isolated by breaking only the
// SessionManager's own captured Sessions store.
func TestSetupSubmitReturnsServerErrorOnLoginFailure(t *testing.T) {
	ts := newTestServer(t)
	ts.App.SessionMgr.Sessions = &models.SessionStore{DB: brokenDB(t)}

	resp, err := ts.Client.PostForm(ts.URL+"/setup", url.Values{
		"name": {"Parent"}, "email": {"login-fail@example.com"}, "password": {"hunter22"}, "confirm_password": {"hunter22"},
	})
	if err != nil {
		t.Fatalf("POST /setup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}
