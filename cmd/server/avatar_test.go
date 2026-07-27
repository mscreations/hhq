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
	"bytes"
	"encoding/base64"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// tinyPNGAvatar is a valid, minimal 1x1 transparent PNG, used as test avatar
// upload bytes.
var tinyPNGAvatar = func() []byte {
	const b64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		panic(err)
	}
	return data
}()

// postAvatarUpload builds and sends a real multipart/form-data POST to
// /parent/users/{id}/avatar, mirroring what the dashboard's upload form
// (hx-encoding="multipart/form-data") actually sends.
func (ts *testServer) postAvatarUpload(t *testing.T, id int, csrfToken string, filename string, data []byte, hx bool) *http.Response {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("avatar", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("writing avatar bytes: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("multipart Close: %v", err)
	}

	// The CSRF token travels as a URL query param, not a multipart field -
	// see the matching comment in web/templates/parent/_children.html.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/parent/users/"+strconv.Itoa(id)+"/avatar?csrf_token="+url.QueryEscape(csrfToken), &body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := ts.Client.Do(req)
	if err != nil {
		t.Fatalf("POST avatar upload: %v", err)
	}
	return resp
}

func TestUploadUserAvatarShowsOnDashboardAndKiosk(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "avatar-upload@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Avatar Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	resp := ts.postAvatarUpload(t, childID, csrfToken, "avatar.png", tinyPNGAvatar, true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (htmx fragment); body: %s", resp.StatusCode, body)
	}

	child, err := ts.App.Users.GetByID(t.Context(), childID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !child.HasAvatar || child.AvatarURL() == "" {
		t.Fatalf("expected avatar to be set after upload, got %+v", child)
	}

	// Kiosk chores fragment should reflect the avatar too, once a chore
	// exists for this child (groupChoresByChild only emits a column when
	// there's at least one chore instance).
	choreID, err := ts.App.Chores.Create(t.Context(), "Test Chore", "")
	if err != nil {
		t.Fatalf("Chores.Create: %v", err)
	}
	if _, err := ts.App.ChoreDefs.CreateRecurring(t.Context(), childID, choreID, 1, 0x7F); err != nil {
		t.Fatalf("ChoreDefs.CreateRecurring: %v", err)
	}

	kioskResp, err := ts.Client.Get(ts.URL + "/kiosk/fragments/chores")
	if err != nil {
		t.Fatalf("GET /kiosk/fragments/chores: %v", err)
	}
	defer kioskResp.Body.Close()
	kioskBody, err := io.ReadAll(kioskResp.Body)
	if err != nil {
		t.Fatalf("reading kiosk fragment: %v", err)
	}
	if !strings.Contains(string(kioskBody), "/avatars/"+strconv.Itoa(childID)) {
		t.Fatalf("expected the kiosk chores fragment to reference the child's avatar URL, got: %s", kioskBody)
	}
}

func TestUploadUserAvatarRejectsNonImage(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "avatar-reject@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Reject Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	resp := ts.postAvatarUpload(t, childID, csrfToken, "not-an-image.txt", []byte("just some text, not an image"), true)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (htmx error fragment)", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	if !strings.Contains(string(body), "unsupported file type") {
		t.Fatalf("expected an inline error mentioning the rejected file type, got: %s", body)
	}

	child, err := ts.App.Users.GetByID(t.Context(), childID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if child.HasAvatar {
		t.Fatal("expected no avatar to be stored for a rejected upload")
	}
}

func TestRemoveUserAvatarFallsBackToColor(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "avatar-remove@example.com", "s3cret-password")

	childID, err := ts.App.Users.CreateChild(t.Context(), "Removable Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if err := ts.App.Users.SetAvatar(t.Context(), childID, tinyPNGAvatar, "image/png"); err != nil {
		t.Fatalf("SetAvatar: %v", err)
	}

	resp := ts.postForm(t, "/parent/users/"+strconv.Itoa(childID)+"/avatar/remove", csrfToken, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 redirect to /parent", resp.StatusCode)
	}

	child, err := ts.App.Users.GetByID(t.Context(), childID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if child.HasAvatar || child.AvatarURL() != "" {
		t.Fatalf("expected avatar to be cleared, got %+v", child)
	}
}

func TestServeAvatarReturnsImageAndSupportsConditionalGet(t *testing.T) {
	ts := newTestServer(t)

	childID, err := ts.App.Users.CreateChild(t.Context(), "Served Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if err := ts.App.Users.SetAvatar(t.Context(), childID, tinyPNGAvatar, "image/png"); err != nil {
		t.Fatalf("SetAvatar: %v", err)
	}

	resp, err := ts.Client.Get(ts.URL + "/avatars/" + strconv.Itoa(childID))
	if err != nil {
		t.Fatalf("GET /avatars/{id}: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("expected an ETag header on the avatar response")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !bytes.Equal(body, tinyPNGAvatar) {
		t.Fatal("served avatar bytes don't match the stored image")
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/avatars/"+strconv.Itoa(childID), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("If-None-Match", etag)
	condResp, err := ts.Client.Do(req)
	if err != nil {
		t.Fatalf("conditional GET: %v", err)
	}
	defer condResp.Body.Close()
	if condResp.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d, want 304 Not Modified for a matching If-None-Match", condResp.StatusCode)
	}
}

// TestUploadParentAvatarTargetsParentsCard is a regression test for the
// avatar routes being shared between children and parents: an upload for a
// parent user must render/target the "_parents" fragment (not "_children"),
// and errors must route through respondParentsError, not
// respondChildrenError.
func TestUploadParentAvatarTargetsParentsCard(t *testing.T) {
	ts := newTestServer(t)
	csrfToken := ts.login(t, "avatar-parent@example.com", "s3cret-password")

	parent, err := ts.App.Users.GetByEmail(t.Context(), "avatar-parent@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}

	resp := ts.postAvatarUpload(t, parent.ID, csrfToken, "avatar.png", tinyPNGAvatar, true)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (htmx fragment); body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `id="parents-card"`) {
		t.Fatalf("expected the response to be the _parents fragment, got: %s", body)
	}
	if strings.Contains(string(body), `id="children-card"`) {
		t.Fatalf("did not expect the _children fragment for a parent avatar upload, got: %s", body)
	}

	updated, err := ts.App.Users.GetByID(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !updated.HasAvatar || updated.AvatarURL() == "" {
		t.Fatalf("expected avatar to be set after upload, got %+v", updated)
	}

	// Now exercise the error path (non-image upload) and confirm it still
	// routes to the Parents card, not Children.
	errResp := ts.postAvatarUpload(t, parent.ID, csrfToken, "not-an-image.txt", []byte("not an image"), true)
	defer errResp.Body.Close()
	errBody, err := io.ReadAll(errResp.Body)
	if err != nil {
		t.Fatalf("reading error response body: %v", err)
	}
	if !strings.Contains(string(errBody), `id="parents-card"`) {
		t.Fatalf("expected the error response to be the _parents fragment, got: %s", errBody)
	}

	// And removal should also target the Parents card.
	rmResp := ts.postForm(t, "/parent/users/"+strconv.Itoa(parent.ID)+"/avatar/remove", csrfToken, nil)
	defer rmResp.Body.Close()
	rmBody, err := io.ReadAll(rmResp.Body)
	if err != nil {
		t.Fatalf("reading remove response body: %v", err)
	}
	if rmResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 redirect to /parent; body: %s", rmResp.StatusCode, rmBody)
	}

	cleared, err := ts.App.Users.GetByID(t.Context(), parent.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if cleared.HasAvatar {
		t.Fatal("expected avatar to be cleared after remove")
	}
}

func TestServeAvatarReturnsNotFoundWhenUnset(t *testing.T) {
	ts := newTestServer(t)

	childID, err := ts.App.Users.CreateChild(t.Context(), "No Avatar Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	resp, err := ts.Client.Get(ts.URL + "/avatars/" + strconv.Itoa(childID))
	if err != nil {
		t.Fatalf("GET /avatars/{id}: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a child with no avatar", resp.StatusCode)
	}
}
