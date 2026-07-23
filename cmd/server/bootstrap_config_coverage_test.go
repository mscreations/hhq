package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/models"
)

// tinyPNG is a valid, minimal 1x1 transparent PNG, used as test avatar_file
// bytes.
var tinyPNG = func() []byte {
	const b64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		panic(err)
	}
	return data
}()

// tinyPNG2 is a second, distinct valid PNG (2x2 instead of 1x1), used to
// test that changing an avatar_file's contents is detected as a change.
var tinyPNG2 = func() []byte {
	const b64 = "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAEklEQVR42mNk+A8EDBAMEiCVAAAmxwT6b3XPKAAAAABJRU5ErkJggg=="
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		panic(err)
	}
	return data
}()

// --- BootstrapChildren remaining branches ---

// TestBootstrapChildrenSkipsEmptyName covers the "name is required"
// validation skip.
func TestBootstrapChildrenSkipsEmptyName(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "", Color: "#3B82F6"}})

	children, err := ts.App.Users.ListChildren(t.Context())
	if err != nil || len(children) != 0 {
		t.Fatalf("expected no child created for an empty name, got children=%+v err=%v", children, err)
	}
}

// TestBootstrapChildrenAutoAssignsColorWhenBlank covers the color=="" ->
// Users.NextAvailableColor branch on create - every existing bootstrap-
// children test supplies an explicit hex color.
func TestBootstrapChildrenAutoAssignsColorWhenBlank(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Auto Color Kid"}})

	children, err := ts.App.Users.ListChildren(t.Context())
	if err != nil || len(children) != 1 {
		t.Fatalf("ListChildren: children=%+v err=%v", children, err)
	}
	if children[0].Color == "" {
		t.Fatal("expected an auto-assigned color, got empty string")
	}
}

// TestBootstrapChildrenResolvesNamedColorOnCreateAndUpdate covers
// ResolveColor's found branch, both when creating a new child and when
// refreshing an existing bootstrap-managed one.
func TestBootstrapChildrenResolvesNamedColorOnCreateAndUpdate(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Named Color Kid", Color: "red"}})
	children, err := ts.App.Users.ListChildren(t.Context())
	if err != nil || len(children) != 1 {
		t.Fatalf("ListChildren: children=%+v err=%v", children, err)
	}
	if children[0].Color != "#EF4444" {
		t.Fatalf("expected the named color 'red' to resolve to #EF4444 on create, got %q", children[0].Color)
	}

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Named Color Kid", Color: "green"}})
	childrenAfter, err := ts.App.Users.ListChildren(t.Context())
	if err != nil || len(childrenAfter) != 1 {
		t.Fatalf("ListChildren after update: children=%+v err=%v", childrenAfter, err)
	}
	if childrenAfter[0].Color != "#22C55E" {
		t.Fatalf("expected the named color 'green' to resolve to #22C55E on update, got %q", childrenAfter[0].Color)
	}
}

// TestBootstrapChildrenKeepsExistingColorOnUpdateWhenBlank covers the
// default branch's "config left color unset: keep whatever it currently is"
// case.
func TestBootstrapChildrenKeepsExistingColorOnUpdateWhenBlank(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Keep Color Kid", Color: "#F59E0B"}})
	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Keep Color Kid", Color: ""}})

	children, err := ts.App.Users.ListChildren(t.Context())
	if err != nil || len(children) != 1 {
		t.Fatalf("ListChildren: children=%+v err=%v", children, err)
	}
	if children[0].Color != "#F59E0B" {
		t.Fatalf("expected the color to be left unchanged, got %q", children[0].Color)
	}
}

// TestBootstrapChildrenListChildrenErrorForRemoval covers the removal
// reconciliation loop's "listing children for removal reconciliation" error
// branch, isolated by using an empty entries list so the per-entry loop
// never touches the (broken) store first.
func TestBootstrapChildrenListChildrenErrorForRemoval(t *testing.T) {
	ts := newTestServer(t)
	orig := ts.App.Users
	ts.App.Users = &models.UserStore{DB: brokenDB(t)}
	defer func() { ts.App.Users = orig }()

	ts.App.BootstrapChildren(t.Context(), nil)
}

// --- BootstrapChildren avatar_file ---

// TestBootstrapChildrenAppliesAvatarFileOnCreate covers avatar_file being
// applied to a freshly-created child.
func TestBootstrapChildrenAppliesAvatarFileOnCreate(t *testing.T) {
	ts := newTestServer(t)
	path := filepath.Join(t.TempDir(), "avatar.png")
	if err := os.WriteFile(path, tinyPNG, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Avatar Kid", AvatarFile: path}})

	child, err := ts.App.Users.GetChildByName(t.Context(), "Avatar Kid")
	if err != nil {
		t.Fatalf("GetChildByName: %v", err)
	}
	if !child.HasAvatar {
		t.Fatal("expected avatar_file to be applied on create")
	}
}

// TestBootstrapChildrenAvatarFileReconcileIsNoopWhenUnchanged covers
// applyChildAvatarFile's checksum comparison: a second reconcile pass with
// the same file contents must not bump avatar_updated_at.
func TestBootstrapChildrenAvatarFileReconcileIsNoopWhenUnchanged(t *testing.T) {
	ts := newTestServer(t)
	path := filepath.Join(t.TempDir(), "avatar.png")
	if err := os.WriteFile(path, tinyPNG, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Stable Avatar Kid", AvatarFile: path}})
	first, err := ts.App.Users.GetChildByName(t.Context(), "Stable Avatar Kid")
	if err != nil {
		t.Fatalf("GetChildByName: %v", err)
	}

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Stable Avatar Kid", AvatarFile: path}})
	second, err := ts.App.Users.GetChildByName(t.Context(), "Stable Avatar Kid")
	if err != nil {
		t.Fatalf("GetChildByName: %v", err)
	}

	if !second.AvatarUpdatedAt.Time.Equal(first.AvatarUpdatedAt.Time) {
		t.Fatalf("expected avatar_updated_at to stay %v on an unchanged reconcile, got %v", first.AvatarUpdatedAt.Time, second.AvatarUpdatedAt.Time)
	}
}

// TestBootstrapChildrenAvatarFileUpdatesWhenFileChanges covers the case
// where the file's on-disk contents actually change between reconcile
// passes - the new bytes must be applied and avatar_updated_at must advance.
func TestBootstrapChildrenAvatarFileUpdatesWhenFileChanges(t *testing.T) {
	ts := newTestServer(t)
	path := filepath.Join(t.TempDir(), "avatar.png")
	if err := os.WriteFile(path, tinyPNG, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Changing Avatar Kid", AvatarFile: path}})
	first, err := ts.App.Users.GetChildByName(t.Context(), "Changing Avatar Kid")
	if err != nil {
		t.Fatalf("GetChildByName: %v", err)
	}
	firstData, _, _, _, err := ts.App.Users.GetAvatarByID(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("GetAvatarByID: %v", err)
	}

	if err := os.WriteFile(path, tinyPNG2, 0o644); err != nil {
		t.Fatalf("WriteFile (updated): %v", err)
	}
	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Changing Avatar Kid", AvatarFile: path}})
	second, err := ts.App.Users.GetChildByName(t.Context(), "Changing Avatar Kid")
	if err != nil {
		t.Fatalf("GetChildByName: %v", err)
	}
	secondData, _, _, _, err := ts.App.Users.GetAvatarByID(t.Context(), second.ID)
	if err != nil {
		t.Fatalf("GetAvatarByID: %v", err)
	}

	if string(firstData) == string(secondData) {
		t.Fatal("expected avatar bytes to change after the file's contents changed")
	}
	if !second.AvatarUpdatedAt.Time.After(first.AvatarUpdatedAt.Time) {
		t.Fatalf("expected avatar_updated_at to advance after the file changed: before=%v after=%v", first.AvatarUpdatedAt.Time, second.AvatarUpdatedAt.Time)
	}
}

// TestBootstrapChildrenAvatarFileOverwritesDashboardUpload covers the
// confirmed "config wins on restart" behavior: a dashboard-uploaded avatar
// on a bootstrap-managed child with avatar_file configured must be
// overwritten by the next reconcile pass.
func TestBootstrapChildrenAvatarFileOverwritesDashboardUpload(t *testing.T) {
	ts := newTestServer(t)
	path := filepath.Join(t.TempDir(), "avatar.png")
	if err := os.WriteFile(path, tinyPNG, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Overwritten Avatar Kid", AvatarFile: path}})
	child, err := ts.App.Users.GetChildByName(t.Context(), "Overwritten Avatar Kid")
	if err != nil {
		t.Fatalf("GetChildByName: %v", err)
	}

	// Simulate a parent uploading a different photo via the dashboard.
	if err := ts.App.Users.SetAvatar(t.Context(), child.ID, tinyPNG2, "image/png"); err != nil {
		t.Fatalf("SetAvatar (simulated dashboard upload): %v", err)
	}
	uploaded, _, _, _, err := ts.App.Users.GetAvatarByID(t.Context(), child.ID)
	if err != nil {
		t.Fatalf("GetAvatarByID: %v", err)
	}
	if string(uploaded) != string(tinyPNG2) {
		t.Fatal("simulated dashboard upload did not take effect")
	}

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Overwritten Avatar Kid", AvatarFile: path}})
	afterReconcile, _, _, _, err := ts.App.Users.GetAvatarByID(t.Context(), child.ID)
	if err != nil {
		t.Fatalf("GetAvatarByID: %v", err)
	}
	if string(afterReconcile) != string(tinyPNG) {
		t.Fatal("expected the next reconcile pass to overwrite the dashboard-uploaded avatar back to avatar_file's image")
	}
}

// TestBootstrapChildrenAvatarFileMissingLogsAndContinues covers an
// unreadable avatar_file: it must be logged and skipped, without aborting
// reconciliation of the rest of children.json (color still applies, and
// later entries are still processed).
func TestBootstrapChildrenAvatarFileMissingLogsAndContinues(t *testing.T) {
	ts := newTestServer(t)
	missingPath := filepath.Join(t.TempDir(), "does-not-exist.png")

	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{
		{Name: "Bad Avatar Kid", Color: "#3B82F6", AvatarFile: missingPath},
		{Name: "Sibling Kid", Color: "#EF4444"},
	})

	badKid, err := ts.App.Users.GetChildByName(t.Context(), "Bad Avatar Kid")
	if err != nil {
		t.Fatalf("GetChildByName(Bad Avatar Kid): %v", err)
	}
	if badKid.Color != "#3B82F6" || badKid.HasAvatar {
		t.Fatalf("expected the color to still apply and no avatar set for a missing avatar_file, got %+v", badKid)
	}

	sibling, err := ts.App.Users.GetChildByName(t.Context(), "Sibling Kid")
	if err != nil {
		t.Fatalf("expected the next entry to still be reconciled after a bad avatar_file: %v", err)
	}
	if sibling.Color != "#EF4444" {
		t.Fatalf("unexpected sibling color: %+v", sibling)
	}
}

// --- BootstrapChores remaining branches ---

// TestBootstrapChoresSkipsEmptyName covers the "name is required" skip.
func TestBootstrapChoresSkipsEmptyName(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapChores(t.Context(), []config.ChoreBootstrap{{Name: "", Description: "no name"}})

	chores, err := ts.App.Chores.ListActive(t.Context())
	if err != nil || len(chores) != 0 {
		t.Fatalf("expected no chore created for an empty name, got chores=%+v err=%v", chores, err)
	}
}

// TestBootstrapChoresListActiveErrorForRemoval isolates the removal loop's
// ListActive error branch the same way TestBootstrapChildrenListChildrenErrorForRemoval
// does.
func TestBootstrapChoresListActiveErrorForRemoval(t *testing.T) {
	ts := newTestServer(t)
	orig := ts.App.Chores
	ts.App.Chores = &models.ChoreStore{DB: brokenDB(t)}
	defer func() { ts.App.Chores = orig }()

	ts.App.BootstrapChores(t.Context(), nil)
}

// TestBootstrapChoresLookupErrorContinues covers the "case err != nil"
// GetByName-failure branch inside the per-entry loop.
func TestBootstrapChoresLookupErrorContinues(t *testing.T) {
	ts := newTestServer(t)
	orig := ts.App.Chores
	ts.App.Chores = &models.ChoreStore{DB: brokenDB(t)}
	defer func() { ts.App.Chores = orig }()

	ts.App.BootstrapChores(t.Context(), []config.ChoreBootstrap{{Name: "Unreachable Chore"}})
}

// --- BootstrapAssignments remaining branches ---

// TestBootstrapAssignmentsSkipsMissingChildOrChore covers the "child and
// chore are both required" validation skip.
func TestBootstrapAssignmentsSkipsMissingChildOrChore(t *testing.T) {
	ts := newTestServer(t)

	ts.App.BootstrapAssignments(t.Context(), []config.AssignmentBootstrap{
		{Child: "", Chore: "Trash", DaysOfWeek: []string{"mon"}},
		{Child: "Someone", Chore: "", DaysOfWeek: []string{"mon"}},
	})

	defs, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defs) != 0 {
		t.Fatalf("expected no assignment created, got defs=%+v err=%v", defs, err)
	}
}

// TestBootstrapAssignmentsSkipsMutuallyExclusiveSchedule covers the
// "days_of_week and one_off_date are mutually exclusive" skip.
func TestBootstrapAssignmentsSkipsMutuallyExclusiveSchedule(t *testing.T) {
	ts := newTestServer(t)
	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Schedule Kid"}})
	ts.App.BootstrapChores(t.Context(), []config.ChoreBootstrap{{Name: "Schedule Chore"}})

	ts.App.BootstrapAssignments(t.Context(), []config.AssignmentBootstrap{
		{Child: "Schedule Kid", Chore: "Schedule Chore", DaysOfWeek: []string{"mon"}, OneOffDate: "2026-08-01"},
	})

	defs, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defs) != 0 {
		t.Fatalf("expected no assignment created for a mutually-exclusive schedule, got defs=%+v err=%v", defs, err)
	}
}

// TestBootstrapAssignmentsSkipsInvalidWeekday covers ResolveWeekday's error
// branch inside the recurring days_of_week loop.
func TestBootstrapAssignmentsSkipsInvalidWeekday(t *testing.T) {
	ts := newTestServer(t)
	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Weekday Kid"}})
	ts.App.BootstrapChores(t.Context(), []config.ChoreBootstrap{{Name: "Weekday Chore"}})

	ts.App.BootstrapAssignments(t.Context(), []config.AssignmentBootstrap{
		{Child: "Weekday Kid", Chore: "Weekday Chore", DaysOfWeek: []string{"notaday"}},
	})

	defs, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defs) != 0 {
		t.Fatalf("expected no assignment created for an invalid weekday, got defs=%+v err=%v", defs, err)
	}
}

// TestBootstrapAssignmentsSkipsInvalidOneOffDate covers the one_off_date
// time.Parse error branch.
func TestBootstrapAssignmentsSkipsInvalidOneOffDate(t *testing.T) {
	ts := newTestServer(t)
	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Date Kid"}})
	ts.App.BootstrapChores(t.Context(), []config.ChoreBootstrap{{Name: "Date Chore"}})

	ts.App.BootstrapAssignments(t.Context(), []config.AssignmentBootstrap{
		{Child: "Date Kid", Chore: "Date Chore", OneOffDate: "not-a-date"},
	})

	defs, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defs) != 0 {
		t.Fatalf("expected no assignment created for an invalid one_off_date, got defs=%+v err=%v", defs, err)
	}
}

// TestBootstrapAssignmentsSkipsMissingSchedule covers the "one of
// days_of_week or one_off_date is required" branch: neither is set.
func TestBootstrapAssignmentsSkipsMissingSchedule(t *testing.T) {
	ts := newTestServer(t)
	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "No Schedule Kid"}})
	ts.App.BootstrapChores(t.Context(), []config.ChoreBootstrap{{Name: "No Schedule Chore"}})

	ts.App.BootstrapAssignments(t.Context(), []config.AssignmentBootstrap{
		{Child: "No Schedule Kid", Chore: "No Schedule Chore"},
	})

	defs, err := ts.App.ChoreDefs.ListActive(t.Context())
	if err != nil || len(defs) != 0 {
		t.Fatalf("expected no assignment created without a schedule, got defs=%+v err=%v", defs, err)
	}
}

// TestBootstrapAssignmentsSkipsNameCollisionWithUIAssignment covers the
// "!existing.BootstrapManaged" skip branch - not covered by any existing
// test (only the analogous child/chore/calendar-account collision tests
// existed before this file).
func TestBootstrapAssignmentsSkipsNameCollisionWithUIAssignment(t *testing.T) {
	ts := newTestServer(t)
	ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Collision Kid"}})
	ts.App.BootstrapChores(t.Context(), []config.ChoreBootstrap{{Name: "Collision Chore"}})

	children, err := ts.App.Users.ListChildren(t.Context())
	if err != nil || len(children) != 1 {
		t.Fatalf("ListChildren: children=%+v err=%v", children, err)
	}
	chores, err := ts.App.Chores.ListActive(t.Context())
	if err != nil || len(chores) != 1 {
		t.Fatalf("ListActive chores: chores=%+v err=%v", chores, err)
	}

	// Created directly (not via BootstrapAssignments), so BootstrapManaged
	// is false on this definition row.
	if _, err := ts.App.ChoreDefs.CreateRecurring(t.Context(), children[0].ID, chores[0].ID, 7, models.WeekdayBit(time.Wednesday)); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	ts.App.BootstrapAssignments(t.Context(), []config.AssignmentBootstrap{
		{Child: "Collision Kid", Chore: "Collision Chore", Points: 99, DaysOfWeek: []string{"mon"}},
	})

	unchanged, err := ts.App.ChoreDefs.GetByChildAndChore(t.Context(), children[0].ID, chores[0].ID)
	if err != nil {
		t.Fatalf("GetByChildAndChore: %v", err)
	}
	if unchanged.Points != 7 || unchanged.BootstrapManaged {
		t.Fatalf("expected the UI-created assignment to be left untouched, got %+v", unchanged)
	}
}

// TestBootstrapAssignmentsLookupErrorsContinue covers the "looking up
// child"/"looking up chore"/"looking up assignment" non-NotFound error
// branches, each isolated to a single store so the preceding lookups on
// other stores still succeed.
func TestBootstrapAssignmentsLookupErrorsContinue(t *testing.T) {
	t.Run("child lookup error", func(t *testing.T) {
		ts := newTestServer(t)
		origUsers := ts.App.Users
		ts.App.Users = &models.UserStore{DB: brokenDB(t)}
		defer func() { ts.App.Users = origUsers }()

		ts.App.BootstrapAssignments(t.Context(), []config.AssignmentBootstrap{
			{Child: "Whoever", Chore: "Whatever", DaysOfWeek: []string{"mon"}},
		})
	})

	t.Run("chore lookup error", func(t *testing.T) {
		ts := newTestServer(t)
		ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Chore Lookup Kid"}})

		origChores := ts.App.Chores
		ts.App.Chores = &models.ChoreStore{DB: brokenDB(t)}
		defer func() { ts.App.Chores = origChores }()

		ts.App.BootstrapAssignments(t.Context(), []config.AssignmentBootstrap{
			{Child: "Chore Lookup Kid", Chore: "Whatever", DaysOfWeek: []string{"mon"}},
		})
	})

	t.Run("assignment lookup error", func(t *testing.T) {
		ts := newTestServer(t)
		ts.App.BootstrapChildren(t.Context(), []config.ChildBootstrap{{Name: "Assignment Lookup Kid"}})
		ts.App.BootstrapChores(t.Context(), []config.ChoreBootstrap{{Name: "Assignment Lookup Chore"}})

		origChoreDefs := ts.App.ChoreDefs
		ts.App.ChoreDefs = &models.ChoreDefinitionStore{DB: brokenDB(t)}
		defer func() { ts.App.ChoreDefs = origChoreDefs }()

		ts.App.BootstrapAssignments(t.Context(), []config.AssignmentBootstrap{
			{Child: "Assignment Lookup Kid", Chore: "Assignment Lookup Chore", DaysOfWeek: []string{"mon"}},
		})
	})
}

// TestBootstrapAssignmentsListActiveErrorForRemoval isolates the removal
// loop's ListActive error branch via an empty entries list.
func TestBootstrapAssignmentsListActiveErrorForRemoval(t *testing.T) {
	ts := newTestServer(t)
	orig := ts.App.ChoreDefs
	ts.App.ChoreDefs = &models.ChoreDefinitionStore{DB: brokenDB(t)}
	defer func() { ts.App.ChoreDefs = orig }()

	ts.App.BootstrapAssignments(t.Context(), nil)
}
