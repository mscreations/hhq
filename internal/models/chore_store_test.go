package models

import (
	"database/sql"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/testutil"
)

func newTestChild(t *testing.T, users *UserStore, name string) int {
	t.Helper()
	id, err := users.CreateChild(t.Context(), name, "#3B82F6")
	if err != nil {
		t.Fatalf("CreateChild: %v", err)
	}
	return id
}

func newTestChore(t *testing.T, conn *sql.DB, name, description string) int {
	t.Helper()
	id, err := (&ChoreStore{DB: conn}).Create(t.Context(), name, description)
	if err != nil {
		t.Fatalf("ChoreStore.Create: %v", err)
	}
	return id
}

func TestChoreDefinitionStoreCreateRecurringAndListActive(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "Take out trash", "Every Mon/Wed")
	mask := WeekdayBit(time.Monday) | WeekdayBit(time.Wednesday)
	id, err := defs.CreateRecurring(ctx, childID, choreID, 5, mask)
	if err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	list, err := defs.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("ListActive = %+v, want the created definition", list)
	}
	if !list[0].DaysOfWeek.Valid || int(list[0].DaysOfWeek.Int32) != mask {
		t.Fatalf("DaysOfWeek = %+v, want mask %d", list[0].DaysOfWeek, mask)
	}
	if list[0].OneOffDate.Valid {
		t.Fatal("expected OneOffDate to be NULL for a recurring chore")
	}
}

func TestChoreDefinitionStoreCreateOneOff(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "Clean garage", "")
	date := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	id, err := defs.CreateOneOff(ctx, childID, choreID, 20, date)
	if err != nil {
		t.Fatalf("CreateOneOff: %v", err)
	}

	list, err := defs.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("ListActive = %+v", list)
	}
	if list[0].DaysOfWeek.Valid {
		t.Fatal("expected DaysOfWeek to be NULL for a one-off chore")
	}
	if !list[0].OneOffDate.Valid || !list[0].OneOffDate.Time.Equal(date) {
		t.Fatalf("OneOffDate = %+v, want %v", list[0].OneOffDate, date)
	}
}

func TestChoreDefinitionStoreDeactivateExcludesFromListActive(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "Chore", "")
	id, err := defs.CreateRecurring(ctx, childID, choreID, 1, WeekdayBit(time.Sunday))
	if err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	if err := defs.Deactivate(ctx, id); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}

	list, err := defs.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected deactivated chore to be excluded, got %+v", list)
	}
}

// TestChoreInstanceStoreEnsureForDateRecurring exercises the mask bitwise-AND
// SQL directly against Postgres (`days_of_week & $2 <> 0`), the same class of
// "does the SQL actually typecheck/behave as intended" risk as round 3's
// make_interval bug.
func TestChoreInstanceStoreEnsureForDateRecurring(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "Wednesday chore", "")
	// A Wednesday-only chore.
	if _, err := defs.CreateRecurring(ctx, childID, choreID, 5, WeekdayBit(time.Wednesday)); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	wednesday := nextWeekday(time.Now(), time.Wednesday)
	thursday := wednesday.AddDate(0, 0, 1)

	if err := instances.EnsureForDate(ctx, wednesday); err != nil {
		t.Fatalf("EnsureForDate(wednesday): %v", err)
	}
	if err := instances.EnsureForDate(ctx, thursday); err != nil {
		t.Fatalf("EnsureForDate(thursday): %v", err)
	}

	wedList, err := instances.ListForDate(ctx, wednesday)
	if err != nil {
		t.Fatalf("ListForDate(wednesday): %v", err)
	}
	if len(wedList) != 1 {
		t.Fatalf("expected 1 instance on Wednesday, got %d", len(wedList))
	}

	thuList, err := instances.ListForDate(ctx, thursday)
	if err != nil {
		t.Fatalf("ListForDate(thursday): %v", err)
	}
	if len(thuList) != 0 {
		t.Fatalf("expected 0 instances on Thursday for a Wednesday-only chore, got %d", len(thuList))
	}
}

func TestChoreInstanceStoreEnsureForDateOneOff(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "One-off chore", "")
	date := time.Now().AddDate(0, 0, 5)
	dateOnly := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	if _, err := defs.CreateOneOff(ctx, childID, choreID, 10, dateOnly); err != nil {
		t.Fatalf("CreateOneOff: %v", err)
	}

	if err := instances.EnsureForDate(ctx, dateOnly); err != nil {
		t.Fatalf("EnsureForDate: %v", err)
	}
	list, err := instances.ListForDate(ctx, dateOnly)
	if err != nil {
		t.Fatalf("ListForDate: %v", err)
	}
	if len(list) != 1 || list[0].ChoreName != "One-off chore" {
		t.Fatalf("ListForDate = %+v", list)
	}
}

func TestChoreInstanceStoreEnsureForDateIsIdempotent(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "Daily chore", "")
	today := time.Now()
	if _, err := defs.CreateRecurring(ctx, childID, choreID, 1, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := instances.EnsureForDate(ctx, today); err != nil {
			t.Fatalf("EnsureForDate call %d: %v", i, err)
		}
	}

	list, err := instances.ListForDate(ctx, today)
	if err != nil {
		t.Fatalf("ListForDate: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly one instance despite calling EnsureForDate 3 times, got %d", len(list))
	}
}

func TestChoreInstanceStoreMarkCompleteFromIncomplete(t *testing.T) {
	conn := testutil.RequireDB(t)
	instances, id := newPendingableInstance(t, conn)

	if _, err := instances.MarkComplete(t.Context(), id); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	got, err := instances.GetByID(t.Context(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != StatusPendingApproval {
		t.Fatalf("Status = %q, want %q", got.Status, StatusPendingApproval)
	}
	if !got.CompletedAt.Valid {
		t.Fatal("expected CompletedAt to be set")
	}
}

// TestChoreInstanceStoreMarkCompleteFromRejectedAllowsRetap is a direct
// regression test for CLAUDE.md round 2, item 1: rejected chores used to be
// dead ends because MarkComplete only accepted the 'incomplete' -> transition.
func TestChoreInstanceStoreMarkCompleteFromRejectedAllowsRetap(t *testing.T) {
	conn := testutil.RequireDB(t)
	instances, id := newPendingableInstance(t, conn)
	ctx := t.Context()

	if _, err := instances.MarkComplete(ctx, id); err != nil {
		t.Fatalf("MarkComplete (initial): %v", err)
	}
	if err := instances.Decide(ctx, id, false, 0); err != nil {
		t.Fatalf("Decide (reject): %v", err)
	}
	got, err := instances.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != StatusRejected {
		t.Fatalf("Status = %q, want %q after rejection", got.Status, StatusRejected)
	}

	if _, err := instances.MarkComplete(ctx, id); err != nil {
		t.Fatalf("MarkComplete after rejection should succeed, got: %v", err)
	}
	got, err = instances.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != StatusPendingApproval {
		t.Fatalf("Status = %q, want %q after re-tapping a rejected chore", got.Status, StatusPendingApproval)
	}
}

func TestChoreInstanceStoreMarkCompleteRejectsInvalidTransition(t *testing.T) {
	conn := testutil.RequireDB(t)
	instances, id := newPendingableInstance(t, conn)
	ctx := t.Context()

	if _, err := instances.MarkComplete(ctx, id); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	// Already pending_approval - tapping again should be rejected, not silently succeed.
	if _, err := instances.MarkComplete(ctx, id); err != ErrInvalidTransition {
		t.Fatalf("MarkComplete on already-pending instance: err = %v, want ErrInvalidTransition", err)
	}
}

func TestChoreInstanceStoreDecideApprove(t *testing.T) {
	conn := testutil.RequireDB(t)
	instances, id := newPendingableInstance(t, conn)
	ctx := t.Context()

	parentID, err := (&UserStore{DB: conn}).CreateParent(ctx, "Deciding Parent", "decider@example.com", "hash")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}

	if _, err := instances.MarkComplete(ctx, id); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	if err := instances.Decide(ctx, id, true, parentID); err != nil {
		t.Fatalf("Decide (approve): %v", err)
	}

	got, err := instances.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != StatusApproved {
		t.Fatalf("Status = %q, want %q", got.Status, StatusApproved)
	}
	if !got.DecidedBy.Valid || int(got.DecidedBy.Int32) != parentID {
		t.Fatalf("DecidedBy = %+v, want %d", got.DecidedBy, parentID)
	}
}

func TestChoreInstanceStoreDecideWithNoParentIDLeavesDecidedByNull(t *testing.T) {
	conn := testutil.RequireDB(t)
	instances, id := newPendingableInstance(t, conn)
	ctx := t.Context()

	if _, err := instances.MarkComplete(ctx, id); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	// parentID = 0 mirrors the no-login email-link approval flow.
	if err := instances.Decide(ctx, id, true, 0); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	got, err := instances.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.DecidedBy.Valid {
		t.Fatalf("DecidedBy = %+v, want NULL when parentID is 0", got.DecidedBy)
	}
}

func TestChoreInstanceStoreDecideRejectsWhenNotPending(t *testing.T) {
	conn := testutil.RequireDB(t)
	instances, id := newPendingableInstance(t, conn)

	// Still 'incomplete' - Decide should only apply to 'pending_approval'.
	if err := instances.Decide(t.Context(), id, true, 1); err != ErrInvalidTransition {
		t.Fatalf("Decide on incomplete instance: err = %v, want ErrInvalidTransition", err)
	}
}

func TestChoreInstanceStoreResetRejected(t *testing.T) {
	conn := testutil.RequireDB(t)
	instances, id := newPendingableInstance(t, conn)
	ctx := t.Context()

	if _, err := instances.MarkComplete(ctx, id); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	if err := instances.Decide(ctx, id, false, 0); err != nil {
		t.Fatalf("Decide (reject): %v", err)
	}
	if err := instances.ResetRejected(ctx, id); err != nil {
		t.Fatalf("ResetRejected: %v", err)
	}

	got, err := instances.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != StatusIncomplete {
		t.Fatalf("Status = %q, want %q", got.Status, StatusIncomplete)
	}
	if got.CompletedAt.Valid || got.DecidedAt.Valid || got.DecidedBy.Valid {
		t.Fatalf("expected all decision fields cleared, got %+v", got)
	}
}

func TestChoreInstanceStoreResetRejectedRejectsWhenNotRejected(t *testing.T) {
	conn := testutil.RequireDB(t)
	instances, id := newPendingableInstance(t, conn)

	if err := instances.ResetRejected(t.Context(), id); err != ErrInvalidTransition {
		t.Fatalf("ResetRejected on incomplete instance: err = %v, want ErrInvalidTransition", err)
	}
}

func TestChoreInstanceStoreListForWeek(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "Daily", "")
	if _, err := defs.CreateRecurring(ctx, childID, choreID, 1, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	weekStart := time.Now().Truncate(24 * time.Hour)
	for i := 0; i < 10; i++ {
		day := weekStart.AddDate(0, 0, i)
		if err := instances.EnsureForDate(ctx, day); err != nil {
			t.Fatalf("EnsureForDate day %d: %v", i, err)
		}
	}

	week, err := instances.ListForWeek(ctx, weekStart)
	if err != nil {
		t.Fatalf("ListForWeek: %v", err)
	}
	if len(week) != 7 {
		t.Fatalf("ListForWeek returned %d instances, want exactly 7 (days 0-6)", len(week))
	}
}

// TestChoreInstanceStoreListActiveForDateCollapsesBacklogIntoOneLateEntry is a
// direct regression test for the kiosk "late chores" feature: a chore still
// unresolved from a previous day plus a fresh instance for today must
// collapse into a SINGLE entry (not one tile per missed day), flagged
// IsLate, and pointing at today's instance so tapping it acts on the current
// day's chore.
func TestChoreInstanceStoreListActiveForDateCollapsesBacklogIntoOneLateEntry(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "Daily chore", "")
	defID, err := defs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111)
	if err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	today := time.Now()
	twoDaysAgo := today.AddDate(0, 0, -2)
	yesterday := today.AddDate(0, 0, -1)
	for _, d := range []time.Time{twoDaysAgo, yesterday, today} {
		if err := instances.EnsureForDate(ctx, d); err != nil {
			t.Fatalf("EnsureForDate(%v): %v", d, err)
		}
	}

	list, err := instances.ListActiveForDate(ctx, today)
	if err != nil {
		t.Fatalf("ListActiveForDate: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected the 3-day backlog to collapse into a single entry, got %+v", list)
	}
	ci := list[0]
	if ci.ChoreDefinitionID != defID {
		t.Fatalf("unexpected chore_definition_id %d", ci.ChoreDefinitionID)
	}
	if !ci.IsLate {
		t.Fatal("expected the collapsed entry to be flagged IsLate given unresolved backlog")
	}
	todayOnly := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	if !ci.DueDate.Equal(todayOnly) {
		t.Fatalf("expected the collapsed entry to represent today's instance (due %v), got due %v", todayOnly, ci.DueDate)
	}
}

// TestChoreInstanceStoreListActiveForDateExcludesResolvedPastInstances ensures
// a past instance that's already approved drops off the active/late list
// entirely once resolved - only today's fresh instance should remain.
func TestChoreInstanceStoreListActiveForDateExcludesResolvedPastInstances(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "Daily chore", "")
	if _, err := defs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	today := time.Now()
	yesterday := today.AddDate(0, 0, -1)
	if err := instances.EnsureForDate(ctx, yesterday); err != nil {
		t.Fatalf("EnsureForDate(yesterday): %v", err)
	}
	yList, err := instances.ListForDate(ctx, yesterday)
	if err != nil || len(yList) != 1 {
		t.Fatalf("ListForDate(yesterday): list=%+v err=%v", yList, err)
	}
	if _, err := instances.MarkComplete(ctx, yList[0].ID); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	if err := instances.Decide(ctx, yList[0].ID, true, 0); err != nil {
		t.Fatalf("Decide (approve): %v", err)
	}

	if err := instances.EnsureForDate(ctx, today); err != nil {
		t.Fatalf("EnsureForDate(today): %v", err)
	}

	list, err := instances.ListActiveForDate(ctx, today)
	if err != nil {
		t.Fatalf("ListActiveForDate: %v", err)
	}
	// Yesterday's instance is now resolved (approved), so it should no longer
	// be returned - only today's fresh instance remains.
	if len(list) != 1 {
		t.Fatalf("expected only today's instance once yesterday's was approved, got %+v", list)
	}
	if list[0].Status != StatusIncomplete || list[0].IsLate {
		t.Fatalf("unexpected remaining instance: %+v", list[0])
	}
}

// TestChoreInstanceStoreDecideApproveCascadesToEarlierUnresolvedInstances is a
// direct regression test for the "completing a late chore catches up the
// backlog" requirement: approving today's instance of a recurring chore must
// also approve any earlier still-unresolved instance of the same definition
// for the same child, so the child isn't forced to redo it once per missed day.
func TestChoreInstanceStoreDecideApproveCascadesToEarlierUnresolvedInstances(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "Daily chore", "")
	if _, err := defs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	today := time.Now()
	twoDaysAgo := today.AddDate(0, 0, -2)
	yesterday := today.AddDate(0, 0, -1)
	var pastIDs []int
	var todayID int
	for _, d := range []time.Time{twoDaysAgo, yesterday, today} {
		if err := instances.EnsureForDate(ctx, d); err != nil {
			t.Fatalf("EnsureForDate(%v): %v", d, err)
		}
		dayList, err := instances.ListForDate(ctx, d)
		if err != nil || len(dayList) != 1 {
			t.Fatalf("ListForDate(%v): list=%+v err=%v", d, dayList, err)
		}
		if d.Equal(today) {
			todayID = dayList[0].ID
		} else {
			pastIDs = append(pastIDs, dayList[0].ID)
		}
	}

	// Collapsing means only a single (late) entry is visible pre-completion.
	before, err := instances.ListActiveForDate(ctx, today)
	if err != nil || len(before) != 1 || !before[0].IsLate {
		t.Fatalf("ListActiveForDate before completion: list=%+v err=%v", before, err)
	}

	if _, err := instances.MarkComplete(ctx, todayID); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	if err := instances.Decide(ctx, todayID, true, 0); err != nil {
		t.Fatalf("Decide (approve): %v", err)
	}

	// Cascade should have approved both past instances too - verify directly
	// via GetByID, since resolved past instances no longer appear in
	// ListActiveForDate (see TestChoreInstanceStoreListActiveForDateExcludesResolvedPastInstances).
	for _, id := range pastIDs {
		got, err := instances.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("GetByID(%d): %v", id, err)
		}
		if got.Status != StatusApproved {
			t.Fatalf("past instance %d has status %q, want %q after cascade", id, got.Status, StatusApproved)
		}
	}

	after, err := instances.ListActiveForDate(ctx, today)
	if err != nil {
		t.Fatalf("ListActiveForDate after completion: %v", err)
	}
	if len(after) != 1 || after[0].ID != todayID || after[0].Status != StatusApproved {
		t.Fatalf("expected only today's approved instance to remain visible, got %+v", after)
	}
}

// newPendingableInstance creates a child, a daily-recurring chore definition,
// and one chore_instance for today in status 'incomplete', returning the
// store and instance ID ready for status-transition tests.
func newPendingableInstance(t *testing.T, conn *sql.DB) (*ChoreInstanceStore, int) {
	t.Helper()
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "Daily chore", "")
	if _, err := defs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}
	today := time.Now()
	if err := instances.EnsureForDate(ctx, today); err != nil {
		t.Fatalf("EnsureForDate: %v", err)
	}
	list, err := instances.ListForDate(ctx, today)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListForDate: list=%+v err=%v", list, err)
	}
	return instances, list[0].ID
}

// newPendingableParentInstance is newPendingableInstance's parent-assignee
// counterpart: a daily-recurring chore assigned to a parent instead of a
// child, worth 0 points (informational only - see
// ChoreDefinition.AssigneeRole).
func newPendingableParentInstance(t *testing.T, conn *sql.DB) (*ChoreInstanceStore, int) {
	t.Helper()
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}
	ctx := t.Context()

	parentID, err := users.CreateParent(ctx, "Mom Real Name", "parentassignee@example.com", "hashed")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	if err := users.SetDisplayName(ctx, parentID, "Mom"); err != nil {
		t.Fatalf("SetDisplayName: %v", err)
	}
	choreID := newTestChore(t, conn, "Dishes", "")
	if _, err := defs.CreateRecurring(ctx, parentID, choreID, 0, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}
	today := time.Now()
	if err := instances.EnsureForDate(ctx, today); err != nil {
		t.Fatalf("EnsureForDate: %v", err)
	}
	list, err := instances.ListForDate(ctx, today)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListForDate: list=%+v err=%v", list, err)
	}
	return instances, list[0].ID
}

// TestChoreInstanceStoreMarkCompleteForParentAssigneeSkipsApproval covers the
// "assign a chore to a parent" feature: tapping it should transition
// straight to 'approved' with no pending_approval step (no email/approval
// needed, since it's informational only).
func TestChoreInstanceStoreMarkCompleteForParentAssigneeSkipsApproval(t *testing.T) {
	conn := testutil.RequireDB(t)
	instances, id := newPendingableParentInstance(t, conn)
	ctx := t.Context()

	status, err := instances.MarkComplete(ctx, id)
	if err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	if status != StatusApproved {
		t.Fatalf("MarkComplete returned status %q, want %q for a parent-assigned chore", status, StatusApproved)
	}

	got, err := instances.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != StatusApproved {
		t.Fatalf("Status = %q, want %q", got.Status, StatusApproved)
	}
	if !got.DecidedAt.Valid {
		t.Fatal("expected DecidedAt to be set")
	}
	if got.DecidedBy.Valid {
		t.Fatalf("expected DecidedBy to stay NULL for a self-completed informational chore, got %+v", got.DecidedBy)
	}
}

// TestChoreInstanceStoreMarkCompleteForParentAssigneeCascadesBacklog mirrors
// TestChoreInstanceStoreDecideApproveCascadesToEarlierUnresolvedInstances,
// but for the parent-assignee path where there's no separate Decide call to
// trigger the cascade.
func TestChoreInstanceStoreMarkCompleteForParentAssigneeCascadesBacklog(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}
	ctx := t.Context()

	parentID, err := users.CreateParent(ctx, "Dad", "backlogparent@example.com", "hashed")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	choreID := newTestChore(t, conn, "Dishes", "")
	if _, err := defs.CreateRecurring(ctx, parentID, choreID, 0, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	today := time.Now()
	yesterday := today.AddDate(0, 0, -1)
	if err := instances.EnsureForDate(ctx, yesterday); err != nil {
		t.Fatalf("EnsureForDate(yesterday): %v", err)
	}
	if err := instances.EnsureForDate(ctx, today); err != nil {
		t.Fatalf("EnsureForDate(today): %v", err)
	}

	yList, err := instances.ListForDate(ctx, yesterday)
	if err != nil || len(yList) != 1 {
		t.Fatalf("ListForDate(yesterday): list=%+v err=%v", yList, err)
	}
	tList, err := instances.ListForDate(ctx, today)
	if err != nil || len(tList) != 1 {
		t.Fatalf("ListForDate(today): list=%+v err=%v", tList, err)
	}

	if _, err := instances.MarkComplete(ctx, tList[0].ID); err != nil {
		t.Fatalf("MarkComplete(today): %v", err)
	}

	gotYesterday, err := instances.GetByID(ctx, yList[0].ID)
	if err != nil {
		t.Fatalf("GetByID(yesterday): %v", err)
	}
	if gotYesterday.Status != StatusApproved {
		t.Fatalf("yesterday's instance status = %q, want %q after completing today's cascades the backlog", gotYesterday.Status, StatusApproved)
	}
}

// TestChoreInstanceStoreListActiveForDateParentAssigneeNeverLate is a direct
// test that unresolved backlog for a parent-assigned (informational) chore
// never sets IsLate, per the "should not be considered late" requirement.
func TestChoreInstanceStoreListActiveForDateParentAssigneeNeverLate(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}
	ctx := t.Context()

	parentID, err := users.CreateParent(ctx, "Dad", "neverlate@example.com", "hashed")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	choreID := newTestChore(t, conn, "Dishes", "")
	if _, err := defs.CreateRecurring(ctx, parentID, choreID, 0, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	today := time.Now()
	yesterday := today.AddDate(0, 0, -1)
	if err := instances.EnsureForDate(ctx, yesterday); err != nil {
		t.Fatalf("EnsureForDate(yesterday): %v", err)
	}
	if err := instances.EnsureForDate(ctx, today); err != nil {
		t.Fatalf("EnsureForDate(today): %v", err)
	}
	// Yesterday's instance is left 'incomplete' (never tapped) - would flag
	// late for a child assignee, but must not for a parent assignee.

	active, err := instances.ListActiveForDate(ctx, today)
	if err != nil {
		t.Fatalf("ListActiveForDate: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("ListActiveForDate = %+v, want exactly one collapsed entry", active)
	}
	if active[0].IsLate {
		t.Fatalf("IsLate = true for a parent-assigned chore, want false regardless of unresolved backlog")
	}
	if active[0].ChildName != "Dad" {
		t.Fatalf("ChildName = %q, want display name fallback %q", active[0].ChildName, "Dad")
	}
}

// TestChoreInstanceStoreListForWeekExcludesParentAssignedChores is a direct
// test for the "should not be included in the weekly report" requirement -
// ListForWeek backs both the weekly PDF report and the dashboard's pending-
// approvals list.
func TestChoreInstanceStoreListForWeekExcludesParentAssignedChores(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	parentID, err := users.CreateParent(ctx, "Dad", "weeklyreportparent@example.com", "hashed")
	if err != nil {
		t.Fatalf("CreateParent: %v", err)
	}
	choreID := newTestChore(t, conn, "Chore", "")
	if _, err := defs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring (child): %v", err)
	}
	if _, err := defs.CreateRecurring(ctx, parentID, choreID, 0, 0b1111111); err != nil {
		t.Fatalf("CreateRecurring (parent): %v", err)
	}

	today := time.Now()
	if err := instances.EnsureForDate(ctx, today); err != nil {
		t.Fatalf("EnsureForDate: %v", err)
	}

	weekStart := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())
	weekStart = weekStart.AddDate(0, 0, -int(weekStart.Weekday()))
	list, err := instances.ListForWeek(ctx, weekStart)
	if err != nil {
		t.Fatalf("ListForWeek: %v", err)
	}
	for _, ci := range list {
		if ci.AssigneeRole == RoleParent {
			t.Fatalf("ListForWeek included a parent-assigned instance: %+v", ci)
		}
	}
	found := false
	for _, ci := range list {
		if ci.ChildID == childID {
			found = true
		}
	}
	if !found {
		t.Fatal("ListForWeek should still include the child-assigned chore")
	}
}

func nextWeekday(from time.Time, target time.Weekday) time.Time {
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	for from.Weekday() != target {
		from = from.AddDate(0, 0, 1)
	}
	return from
}

func TestChoreStoreGetByNameNotFound(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &ChoreStore{DB: conn}

	if _, err := s.GetByName(t.Context(), "Nonexistent"); err != ErrNotFound {
		t.Fatalf("GetByName for missing chore: err = %v, want ErrNotFound", err)
	}
}

func TestChoreStoreCreateBootstrapAndUpdate(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &ChoreStore{DB: conn}
	ctx := t.Context()

	id, err := s.CreateBootstrap(ctx, "Bootstrap Chore", "orig description")
	if err != nil {
		t.Fatalf("CreateBootstrap: %v", err)
	}

	got, err := s.GetByName(ctx, "Bootstrap Chore")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.ID != id || !got.BootstrapManaged || got.Description.String != "orig description" {
		t.Fatalf("unexpected chore: %+v", got)
	}

	if err := s.UpdateBootstrap(ctx, id, "new description"); err != nil {
		t.Fatalf("UpdateBootstrap: %v", err)
	}
	updated, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.Description.String != "new description" || updated.Name != "Bootstrap Chore" || !updated.BootstrapManaged {
		t.Fatalf("UpdateBootstrap did not apply as expected: %+v", updated)
	}
}

func TestChoreDefinitionStoreGetByChildAndChoreNotFound(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "Unassigned Chore", "")

	if _, err := defs.GetByChildAndChore(ctx, childID, choreID); err != ErrNotFound {
		t.Fatalf("GetByChildAndChore for unassigned pair: err = %v, want ErrNotFound", err)
	}
}

func TestChoreDefinitionStoreBootstrapCreateAndSwitchSchedule(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "Bootstrap Assignment Chore", "")

	mask := WeekdayBit(time.Monday)
	id, err := defs.CreateRecurringBootstrap(ctx, childID, choreID, 5, mask)
	if err != nil {
		t.Fatalf("CreateRecurringBootstrap: %v", err)
	}

	got, err := defs.GetByChildAndChore(ctx, childID, choreID)
	if err != nil {
		t.Fatalf("GetByChildAndChore: %v", err)
	}
	if got.ID != id || !got.BootstrapManaged || !got.DaysOfWeek.Valid || int(got.DaysOfWeek.Int32) != mask || got.OneOffDate.Valid {
		t.Fatalf("unexpected assignment after CreateRecurringBootstrap: %+v", got)
	}

	// Switching to a one-off schedule must clear days_of_week to satisfy the
	// recurring_xor_one_off CHECK constraint.
	oneOff := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if err := defs.UpdateBootstrapOneOff(ctx, id, 3, oneOff); err != nil {
		t.Fatalf("UpdateBootstrapOneOff: %v", err)
	}
	afterOneOff, err := defs.GetByChildAndChore(ctx, childID, choreID)
	if err != nil {
		t.Fatalf("GetByChildAndChore after UpdateBootstrapOneOff: %v", err)
	}
	if afterOneOff.Points != 3 || afterOneOff.DaysOfWeek.Valid || !afterOneOff.OneOffDate.Valid || !afterOneOff.OneOffDate.Time.Equal(oneOff) {
		t.Fatalf("unexpected assignment after UpdateBootstrapOneOff: %+v", afterOneOff)
	}

	// And switching back to recurring must clear one_off_date.
	newMask := WeekdayBit(time.Friday)
	if err := defs.UpdateBootstrapRecurring(ctx, id, 7, newMask); err != nil {
		t.Fatalf("UpdateBootstrapRecurring: %v", err)
	}
	afterRecurring, err := defs.GetByChildAndChore(ctx, childID, choreID)
	if err != nil {
		t.Fatalf("GetByChildAndChore after UpdateBootstrapRecurring: %v", err)
	}
	if afterRecurring.Points != 7 || afterRecurring.OneOffDate.Valid || !afterRecurring.DaysOfWeek.Valid || int(afterRecurring.DaysOfWeek.Int32) != newMask {
		t.Fatalf("unexpected assignment after UpdateBootstrapRecurring: %+v", afterRecurring)
	}
}

func TestChoreDefinitionStoreCreateOneOffBootstrap(t *testing.T) {
	conn := testutil.RequireDB(t)
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID := newTestChore(t, conn, "One Off Bootstrap Chore", "")
	date := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

	id, err := defs.CreateOneOffBootstrap(ctx, childID, choreID, 2, date)
	if err != nil {
		t.Fatalf("CreateOneOffBootstrap: %v", err)
	}

	got, err := defs.GetByChildAndChore(ctx, childID, choreID)
	if err != nil {
		t.Fatalf("GetByChildAndChore: %v", err)
	}
	if got.ID != id || !got.BootstrapManaged || got.DaysOfWeek.Valid || !got.OneOffDate.Valid || !got.OneOffDate.Time.Equal(date) {
		t.Fatalf("unexpected assignment after CreateOneOffBootstrap: %+v", got)
	}
}
