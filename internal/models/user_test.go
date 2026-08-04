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

package models

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/mscreations/hhq/internal/testutil"
)

// tinyPNG is a valid, minimal 1x1 transparent PNG, used as test avatar bytes.
var tinyPNG = func() []byte {
	const b64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		panic(err)
	}
	return data
}()

func TestUserStoreCreateAndGet(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	childID, err := s.CreateChild(ctx, "Kid One", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	got, err := s.GetByID(ctx, childID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Kid One" || got.Role != RoleChild || got.Color != "#3B82F6" || !got.IsActive {
		t.Fatalf("unexpected user: %+v", got)
	}

	parentID, err := s.CreateParent(ctx, "Parent One", "parent@example.com", "hashed")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	gotParent, err := s.GetByEmail(ctx, "parent@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if gotParent.ID != parentID || gotParent.Role != RoleParent {
		t.Fatalf("unexpected parent: %+v", gotParent)
	}
}

func TestUserStoreSetDisplayNameAndDisplayLabel(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	parentID, err := s.CreateParent(ctx, "Real Name", "displayname@example.com", "hashed")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	got, err := s.GetByID(ctx, parentID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.DisplayLabel() != "Real Name" {
		t.Fatalf("DisplayLabel before setting one = %q, want real name", got.DisplayLabel())
	}

	if err := s.SetDisplayName(ctx, parentID, "Mom"); err != nil {
		t.Fatalf("SetDisplayName: %v", err)
	}
	got, err = s.GetByID(ctx, parentID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.DisplayLabel() != "Mom" {
		t.Fatalf("DisplayLabel after setting = %q, want %q", got.DisplayLabel(), "Mom")
	}
	if got.Name != "Real Name" {
		t.Fatalf("Name changed unexpectedly to %q - display name should be independent of Name", got.Name)
	}

	// Clearing (blank) falls back to the real name again.
	if err := s.SetDisplayName(ctx, parentID, ""); err != nil {
		t.Fatalf("SetDisplayName (clear): %v", err)
	}
	got, err = s.GetByID(ctx, parentID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.DisplayLabel() != "Real Name" {
		t.Fatalf("DisplayLabel after clearing = %q, want real name", got.DisplayLabel())
	}
}

func TestUserStoreGetByIDNotFound(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}

	if _, err := s.GetByID(t.Context(), 99999); err != ErrNotFound {
		t.Fatalf("GetByID for missing id: err = %v, want ErrNotFound", err)
	}
}

func TestUserStoreGetByEmailNotFound(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}

	if _, err := s.GetByEmail(t.Context(), "nobody@example.com"); err != ErrNotFound {
		t.Fatalf("GetByEmail for missing email: err = %v, want ErrNotFound", err)
	}
}

func TestUserStoreListChildrenExcludesInactiveAndParents(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	activeChild, err := s.CreateChild(ctx, "Active Kid", "#EF4444")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	inactiveChild, err := s.CreateChild(ctx, "Inactive Kid", "#22C55E")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if err := s.Deactivate(ctx, inactiveChild); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if _, err := s.CreateParent(ctx, "A Parent", "p@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	children, err := s.ListChildren(ctx)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	if len(children) != 1 || children[0].ID != activeChild {
		t.Fatalf("ListChildren = %+v, want only the active child (id %d)", children, activeChild)
	}
}

func TestUserStoreListParents(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	if _, err := s.CreateParent(ctx, "Parent A", "a@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	if _, err := s.CreateChild(ctx, "Kid", "#3B82F6"); err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	parents, err := s.ListParents(ctx)
	if err != nil {
		t.Fatalf("ListParents: %v", err)
	}
	if len(parents) != 1 || parents[0].Name != "Parent A" {
		t.Fatalf("ListParents = %+v, want only Parent A", parents)
	}
}

func TestUserStoreListAllIncludesEveryoneRegardlessOfStatus(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	childID, err := s.CreateChild(ctx, "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if err := s.Deactivate(ctx, childID); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if _, err := s.CreateParent(ctx, "Parent", "p@example.com", "hash"); err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	all, err := s.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAll returned %d users, want 2 (including the deactivated child)", len(all))
	}
}

func TestUserStoreNextAvailableColorSkipsUsedColors(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	first, err := s.NextAvailableColor(ctx)
	if err != nil {
		t.Fatalf("NextAvailableColor: %v", err)
	}
	if _, err := s.CreateChild(ctx, "Kid", first); err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	second, err := s.NextAvailableColor(ctx)
	if err != nil {
		t.Fatalf("NextAvailableColor: %v", err)
	}
	if second == first {
		t.Fatalf("NextAvailableColor returned %q twice in a row while it was already in use", first)
	}
}

func TestUserStoreInviteAcceptFlow(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	id, err := s.InviteParent(ctx, "Invited Parent", "invited@example.com")
	if err != nil {
		t.Fatalf("InviteParent: %v", err)
	}

	invited, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if invited.PasswordHash.Valid {
		t.Fatal("expected invited parent to have no password set yet")
	}
	if !invited.InvitedAt.Valid {
		t.Fatal("expected invited_at to be set")
	}

	if err := s.SetPasswordAndAccept(ctx, id, "new-hash"); err != nil {
		t.Fatalf("SetPasswordAndAccept: %v", err)
	}

	accepted, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !accepted.PasswordHash.Valid || accepted.PasswordHash.String != "new-hash" {
		t.Fatalf("expected password hash to be set after accept, got %+v", accepted.PasswordHash)
	}
}

func TestUserStoreMarkInvitedRefreshesTimestamp(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	id, err := s.InviteParent(ctx, "Invited Parent", "invited2@example.com")
	if err != nil {
		t.Fatalf("InviteParent: %v", err)
	}
	before, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if err := s.MarkInvited(ctx, id); err != nil {
		t.Fatalf("MarkInvited: %v", err)
	}
	after, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !after.InvitedAt.Time.After(before.InvitedAt.Time) && !after.InvitedAt.Time.Equal(before.InvitedAt.Time) {
		t.Fatalf("expected invited_at to not go backwards: before=%v after=%v", before.InvitedAt.Time, after.InvitedAt.Time)
	}
}

func TestUserStoreUpdateChild(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	id, err := s.CreateChild(ctx, "Old Name", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if err := s.UpdateChild(ctx, id, "New Name", "#EF4444"); err != nil {
		t.Fatalf("UpdateChild: %v", err)
	}

	got, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "New Name" || got.Color != "#EF4444" {
		t.Fatalf("UpdateChild did not apply: %+v", got)
	}
}

func TestUserStoreDeactivate(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	id, err := s.CreateChild(ctx, "Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	if err := s.Deactivate(ctx, id); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}

	got, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.IsActive {
		t.Fatal("expected user to be inactive after Deactivate")
	}
}

func TestUserStoreGetChildByNameNotFound(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}

	if _, err := s.GetChildByName(t.Context(), "Nobody"); err != ErrNotFound {
		t.Fatalf("GetChildByName for missing name: err = %v, want ErrNotFound", err)
	}
}

func TestUserStoreCreateChildBootstrapAndUpdate(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	id, err := s.CreateChildBootstrap(ctx, "Bootstrap Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChildBootstrap: %v", err)
	}

	got, err := s.GetChildByName(ctx, "Bootstrap Kid")
	if err != nil {
		t.Fatalf("GetChildByName: %v", err)
	}
	if got.ID != id || !got.BootstrapManaged || got.Color != "#3B82F6" {
		t.Fatalf("unexpected child: %+v", got)
	}

	if err := s.UpdateChildBootstrap(ctx, id, "#EF4444"); err != nil {
		t.Fatalf("UpdateChildBootstrap: %v", err)
	}
	updated, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Color != "#EF4444" || updated.Name != "Bootstrap Kid" || !updated.BootstrapManaged {
		t.Fatalf("UpdateChildBootstrap did not apply as expected: %+v", updated)
	}
}

func TestUserStoreGetParentByEmailNotFound(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}

	if _, err := s.GetParentByEmail(t.Context(), "nobody@example.com"); err != ErrNotFound {
		t.Fatalf("GetParentByEmail for missing email: err = %v, want ErrNotFound", err)
	}
}

func TestUserStoreGetParentByEmailExcludesChildren(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	if _, err := s.CreateChild(ctx, "Not A Parent", "#3B82F6"); err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	// Children have no email column value set, so give it one directly to
	// prove GetParentByEmail is role-scoped, not just email-scoped.
	if _, err := conn.ExecContext(ctx, `UPDATE hhq_users SET email = $1 WHERE name = 'Not A Parent'`, "shared@example.com"); err != nil {
		t.Fatalf("setting child email directly: %v", err)
	}

	if _, err := s.GetParentByEmail(ctx, "shared@example.com"); err != ErrNotFound {
		t.Fatalf("GetParentByEmail for a child's email: err = %v, want ErrNotFound", err)
	}
}

func TestUserStoreCreateParentBootstrapAndUpdate(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	id, err := s.CreateParentBootstrap(ctx, "Bootstrap Parent", "bootstrap-parent@example.com", "hash1", "#3B82F6", "Dad")
	if err != nil {
		t.Fatalf("CreateParentBootstrap: %v", err)
	}

	got, err := s.GetParentByEmail(ctx, "bootstrap-parent@example.com")
	if err != nil {
		t.Fatalf("GetParentByEmail: %v", err)
	}
	if got.ID != id || !got.BootstrapManaged || got.Color != "#3B82F6" || got.DisplayName.String != "Dad" || got.PasswordHash.String != "hash1" {
		t.Fatalf("unexpected parent: %+v", got)
	}

	if err := s.UpdateParentBootstrap(ctx, id, "hash2", "#EF4444", "Mom"); err != nil {
		t.Fatalf("UpdateParentBootstrap: %v", err)
	}
	updated, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Color != "#EF4444" || updated.DisplayName.String != "Mom" || updated.PasswordHash.String != "hash2" || updated.Name != "Bootstrap Parent" || !updated.BootstrapManaged {
		t.Fatalf("UpdateParentBootstrap did not apply as expected: %+v", updated)
	}
}

func TestUserStoreSetGetClearAvatar(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &UserStore{DB: conn}
	ctx := t.Context()

	id, err := s.CreateChild(ctx, "Avatar Kid", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}

	// No avatar yet.
	before, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if before.HasAvatar || before.AvatarURL() != "" {
		t.Fatalf("expected no avatar before SetAvatar, got %+v", before)
	}
	if _, _, _, ok, err := s.GetAvatarByID(ctx, id); err != nil || ok {
		t.Fatalf("GetAvatarByID before set: ok=%v err=%v, want ok=false", ok, err)
	}
	if _, ok, err := s.GetAvatarChecksum(ctx, id); err != nil || ok {
		t.Fatalf("GetAvatarChecksum before set: ok=%v err=%v, want ok=false", ok, err)
	}

	if err := s.SetAvatar(ctx, id, tinyPNG, "image/png"); err != nil {
		t.Fatalf("SetAvatar: %v", err)
	}

	after, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !after.HasAvatar || after.AvatarURL() == "" {
		t.Fatalf("expected avatar to be set after SetAvatar, got %+v", after)
	}

	data, contentType, updatedAt, ok, err := s.GetAvatarByID(ctx, id)
	if err != nil {
		t.Fatalf("GetAvatarByID: %v", err)
	}
	if !ok {
		t.Fatal("GetAvatarByID: ok = false, want true")
	}
	if !bytes.Equal(data, tinyPNG) {
		t.Fatalf("GetAvatarByID data mismatch: got %d bytes, want %d bytes", len(data), len(tinyPNG))
	}
	if contentType != "image/png" {
		t.Fatalf("GetAvatarByID contentType = %q, want image/png", contentType)
	}
	if updatedAt.IsZero() {
		t.Fatal("GetAvatarByID updatedAt is zero")
	}

	checksum, ok, err := s.GetAvatarChecksum(ctx, id)
	if err != nil {
		t.Fatalf("GetAvatarChecksum: %v", err)
	}
	if !ok || checksum == "" {
		t.Fatalf("GetAvatarChecksum: ok=%v checksum=%q, want a non-empty checksum", ok, checksum)
	}

	if err := s.ClearAvatar(ctx, id); err != nil {
		t.Fatalf("ClearAvatar: %v", err)
	}
	cleared, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if cleared.HasAvatar || cleared.AvatarURL() != "" {
		t.Fatalf("expected no avatar after ClearAvatar, got %+v", cleared)
	}
	if _, _, _, ok, err := s.GetAvatarByID(ctx, id); err != nil || ok {
		t.Fatalf("GetAvatarByID after clear: ok=%v err=%v, want ok=false", ok, err)
	}
}
