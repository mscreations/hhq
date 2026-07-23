package main

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/models"
)

// TestLoginSubmitReturnsServerErrorOnSessionCreationFailure covers
// LoginSubmit's SessionMgr.Login error branch, isolated by breaking only
// the SessionManager's own captured Sessions store (Users, used by
// GetByEmail just before, stays intact).
func TestLoginSubmitReturnsServerErrorOnSessionCreationFailure(t *testing.T) {
	ts := newTestServer(t)
	hash, err := auth.HashPassword("hunter22")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := ts.App.Users.CreateParent(t.Context(), "Parent", "login-session-fail@example.com", hash); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	ts.App.SessionMgr.Sessions = &models.SessionStore{DB: brokenDB(t)}

	resp, err := ts.Client.PostForm(ts.URL+"/login", url.Values{
		"email": {"login-session-fail@example.com"}, "password": {"hunter22"},
	})
	if err != nil {
		t.Fatalf("POST /login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}
