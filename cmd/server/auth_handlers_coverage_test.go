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
