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
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/models"
)

// TestBootstrapParentsCreatesAndUpsertsOnRerun mirrors
// TestBootstrapChildrenCreatesAndUpsertsOnRerun: first run creates a
// bootstrap-managed parent; a second run with a changed password/color
// updates the same row rather than duplicating it, and the new password
// actually authenticates.
func TestBootstrapParentsCreatesAndUpsertsOnRerun(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapParents(t.Context(), []config.ParentBootstrap{
		{Name: "Bootstrap Parent", Email: "bootstrap-parent@example.com", Password: "first-password", Color: "#3B82F6"},
	})

	parents, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parents) != 1 {
		t.Fatalf("ListParents after first run: parents=%+v err=%v", parents, err)
	}
	first := parents[0]
	if !first.BootstrapManaged || first.Color != "#3B82F6" || first.Email.String != "bootstrap-parent@example.com" {
		t.Fatalf("unexpected parent after first run: %+v", first)
	}
	if !auth.CheckPassword(first.PasswordHash.String, "first-password") {
		t.Fatal("expected the first password to authenticate")
	}

	ts.App.BootstrapParents(t.Context(), []config.ParentBootstrap{
		{Name: "Bootstrap Parent", Email: "bootstrap-parent@example.com", Password: "second-password", Color: "#EF4444"},
	})

	parentsAfter, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parentsAfter) != 1 {
		t.Fatalf("ListParents after second run: parents=%+v err=%v", parentsAfter, err)
	}
	second := parentsAfter[0]
	if second.ID != first.ID {
		t.Fatalf("expected the same row to be updated, got a new id %d (was %d)", second.ID, first.ID)
	}
	if second.Color != "#EF4444" {
		t.Fatalf("expected color to be refreshed to #EF4444, got %q", second.Color)
	}
	if !auth.CheckPassword(second.PasswordHash.String, "second-password") {
		t.Fatal("expected the refreshed password to authenticate")
	}
	if auth.CheckPassword(second.PasswordHash.String, "first-password") {
		t.Fatal("expected the old password to no longer authenticate")
	}
}

// TestBootstrapParentsKeepsExistingDisplayNameAndColorWhenBlank covers the
// "config left color/display_name unset: keep whatever it currently is"
// branches on update.
func TestBootstrapParentsKeepsExistingDisplayNameAndColorWhenBlank(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapParents(t.Context(), []config.ParentBootstrap{
		{Name: "Keep Parent", Email: "keep-parent@example.com", Password: "pw", Color: "#F59E0B", DisplayName: "Dad"},
	})
	ts.App.BootstrapParents(t.Context(), []config.ParentBootstrap{
		{Name: "Keep Parent", Email: "keep-parent@example.com", Password: "pw2"},
	})

	parents, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parents) != 1 {
		t.Fatalf("ListParents: parents=%+v err=%v", parents, err)
	}
	if parents[0].Color != "#F59E0B" {
		t.Fatalf("expected color to be left unchanged, got %q", parents[0].Color)
	}
	if parents[0].DisplayName.String != "Dad" {
		t.Fatalf("expected display name to be left unchanged, got %q", parents[0].DisplayName.String)
	}
}

// TestBootstrapParentsRemovesEntryDroppedFromConfig mirrors
// TestBootstrapChildrenRemovesEntryDroppedFromConfig.
func TestBootstrapParentsRemovesEntryDroppedFromConfig(t *testing.T) {
	ts := newTestServer(t)

	entries := []config.ParentBootstrap{
		{Name: "Keep Parent", Email: "keep2@example.com", Password: "pw"},
		{Name: "Drop Parent", Email: "drop@example.com", Password: "pw"},
	}
	ts.App.BootstrapParents(t.Context(), entries)

	parents, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parents) != 2 {
		t.Fatalf("ListParents after first run: parents=%+v err=%v", parents, err)
	}

	ts.App.BootstrapParents(t.Context(), entries[:1])

	parentsAfter, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parentsAfter) != 1 {
		t.Fatalf("ListParents after removal: parents=%+v err=%v", parentsAfter, err)
	}
	if parentsAfter[0].Email.String != "keep2@example.com" {
		t.Fatalf("expected only the kept parent to remain, got %+v", parentsAfter)
	}
}

// TestBootstrapParentsRefusesToRemoveLastRemainingParent is the key
// new-behavior case: pruning must never deactivate the last active parent,
// even if parents.json no longer lists them, so the app never locks
// everyone out.
func TestBootstrapParentsRefusesToRemoveLastRemainingParent(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapParents(t.Context(), []config.ParentBootstrap{
		{Name: "Only Parent", Email: "only@example.com", Password: "pw"},
	})
	parents, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parents) != 1 {
		t.Fatalf("ListParents after first run: parents=%+v err=%v", parents, err)
	}

	// Re-run with an empty list - this parent is no longer in parents.json,
	// but is the only remaining active parent.
	ts.App.BootstrapParents(t.Context(), nil)

	parentsAfter, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parentsAfter) != 1 {
		t.Fatalf("expected the last remaining parent to survive an empty parents.json, got parents=%+v err=%v", parentsAfter, err)
	}
}

// TestBootstrapParentsRemovesOneOfTwoWhenBothDropped verifies the guard
// above only protects the *last* parent - with two active parents, dropping
// one from the config does deactivate it.
func TestBootstrapParentsRemovesOneOfTwoWhenBothDropped(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapParents(t.Context(), []config.ParentBootstrap{
		{Name: "First Parent", Email: "first@example.com", Password: "pw"},
		{Name: "Second Parent", Email: "second@example.com", Password: "pw"},
	})

	ts.App.BootstrapParents(t.Context(), []config.ParentBootstrap{
		{Name: "Second Parent", Email: "second@example.com", Password: "pw"},
	})

	parents, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parents) != 1 {
		t.Fatalf("expected exactly one parent to remain, got parents=%+v err=%v", parents, err)
	}
	if parents[0].Email.String != "second@example.com" {
		t.Fatalf("expected the still-configured parent to remain, got %+v", parents[0])
	}
}

// TestBootstrapParentsSkipsCollisionWithInvitedParent covers the
// "!existing.BootstrapManaged" skip branch: a parent invited via the
// dashboard with the same email is left untouched.
func TestBootstrapParentsSkipsCollisionWithInvitedParent(t *testing.T) {
	ts := newTestServer(t)

	uiID, err := ts.App.Users.InviteParent(t.Context(), "UI Parent", "shared@example.com")
	if err != nil {
		t.Fatalf("InviteParent: %v", err)
	}

	ts.App.BootstrapParents(t.Context(), []config.ParentBootstrap{
		{Name: "Config Parent", Email: "shared@example.com", Password: "pw"},
	})

	unchanged, err := ts.App.Users.GetByID(t.Context(), uiID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if unchanged.Name != "UI Parent" || unchanged.BootstrapManaged {
		t.Fatalf("expected the dashboard-invited parent to be left untouched, got %+v", unchanged)
	}
}

// TestBootstrapParentsAppliesAvatarFile covers avatar_file being applied to
// a freshly-created parent.
func TestBootstrapParentsAppliesAvatarFile(t *testing.T) {
	ts := newTestServer(t)
	path := filepath.Join(t.TempDir(), "avatar.png")
	if err := os.WriteFile(path, tinyPNG, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ts.App.BootstrapParents(t.Context(), []config.ParentBootstrap{
		{Name: "Avatar Parent", Email: "avatar-parent@example.com", Password: "pw", AvatarFile: path},
	})

	parent, err := ts.App.Users.GetParentByEmail(t.Context(), "avatar-parent@example.com")
	if err != nil {
		t.Fatalf("GetParentByEmail: %v", err)
	}
	if !parent.HasAvatar {
		t.Fatal("expected avatar_file to be applied on create")
	}
}

// TestBootstrapParentsSkipsInvalidEntries covers the per-entry validation
// skips (missing name/email/password) - none should create a row, and a
// valid entry after an invalid one is still processed.
func TestBootstrapParentsSkipsInvalidEntries(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapParents(t.Context(), []config.ParentBootstrap{
		{Name: "", Email: "noname@example.com", Password: "pw"},
		{Name: "No Email", Password: "pw"},
		{Name: "No Password", Email: "nopassword@example.com"},
		{Name: "Both Set", Email: "both@example.com", EmailFile: "/does/not/matter", Password: "pw"},
		{Name: "Valid Parent", Email: "valid@example.com", Password: "pw"},
	})

	parents, err := ts.App.Users.ListParents(t.Context())
	if err != nil || len(parents) != 1 {
		t.Fatalf("expected only the valid entry to create a parent, got parents=%+v err=%v", parents, err)
	}
	if parents[0].Email.String != "valid@example.com" {
		t.Fatalf("unexpected parent: %+v", parents[0])
	}
}

// TestBootstrapParentsListParentsErrorForRemoval isolates the removal loop's
// ListParents error branch via an empty entries list, mirroring
// TestBootstrapChildrenListChildrenErrorForRemoval.
func TestBootstrapParentsListParentsErrorForRemoval(t *testing.T) {
	ts := newTestServer(t)
	orig := ts.App.Users
	ts.App.Users = &models.UserStore{DB: brokenDB(t)}
	defer func() { ts.App.Users = orig }()

	ts.App.BootstrapParents(t.Context(), nil)
}

// TestSetParentDisplayNameAndRemoveUserRejectBootstrapManagedParent covers
// the dashboard-side 403 gating: a bootstrap-managed parent can't have its
// display name changed or be removed through the dashboard, since either
// would just get silently clobbered/recreated on the next restart.
func TestSetParentDisplayNameAndRemoveUserRejectBootstrapManagedParent(t *testing.T) {
	ts := newTestServer(t)
	ts.App.BootstrapParents(t.Context(), []config.ParentBootstrap{
		{Name: "Managed Parent", Email: "managed-parent@example.com", Password: "pw"},
	})
	parent, err := ts.App.Users.GetParentByEmail(t.Context(), "managed-parent@example.com")
	if err != nil {
		t.Fatalf("GetParentByEmail: %v", err)
	}

	// Log in as a second, non-managed parent so there's an authenticated
	// session to drive these dashboard requests with.
	csrfToken := ts.login(t, "dashboard-parent@example.com", "some-password")

	resp := ts.postForm(t, "/parent/users/"+strconv.Itoa(parent.ID)+"/display-name", csrfToken, url.Values{"display_name": {"Nope"}})
	if resp.StatusCode != 403 {
		t.Fatalf("SetParentDisplayName on a bootstrap-managed parent: status = %d, want 403", resp.StatusCode)
	}

	resp2 := ts.postForm(t, "/parent/users/"+strconv.Itoa(parent.ID)+"/remove", csrfToken, nil)
	if resp2.StatusCode != 403 {
		t.Fatalf("RemoveUser on a bootstrap-managed parent: status = %d, want 403", resp2.StatusCode)
	}
}
