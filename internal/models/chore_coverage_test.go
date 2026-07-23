package models

import (
	"context"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/testutil"
)

// TestChoreStoreListActive covers ChoreStore.ListActive (the catalog-level
// list, distinct from ChoreDefinitionStore.ListActive which already has
// coverage) - previously untested entirely.
func TestChoreStoreListActive(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &ChoreStore{DB: conn}
	ctx := t.Context()

	activeID, err := s.Create(ctx, "Active Chore", "do it")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	inactiveID, err := s.Create(ctx, "Inactive Chore", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Deactivate(ctx, inactiveID); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}

	list, err := s.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(list) != 1 || list[0].ID != activeID {
		t.Fatalf("ListActive = %+v, want only the active chore (id %d)", list, activeID)
	}
}

// TestChoreStoreUpdate covers ChoreStore.Update (rename/redescribe a catalog
// chore), previously untested.
func TestChoreStoreUpdate(t *testing.T) {
	conn := testutil.RequireDB(t)
	s := &ChoreStore{DB: conn}
	ctx := t.Context()

	id, err := s.Create(ctx, "Old Name", "old description")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.Update(ctx, id, "New Name", "new description"); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "New Name" || got.Description.String != "new description" {
		t.Fatalf("Update did not apply: %+v", got)
	}
}

// TestChoreStoreDeactivateCascades is a direct test of ChoreStore.Deactivate's
// transaction: deactivating a catalog chore must deactivate every
// chore_definition assigned to it AND delete any of their unresolved
// (incomplete/pending_approval/rejected) instances, while leaving an already
// approved instance untouched - mirroring ChoreDefinitionStore.Deactivate's
// own cascade, one level up the hierarchy. Previously entirely untested.
func TestChoreStoreDeactivateCascades(t *testing.T) {
	conn := testutil.RequireDB(t)
	chores := &ChoreStore{DB: conn}
	users := &UserStore{DB: conn}
	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}
	ctx := t.Context()

	childID := newTestChild(t, users, "Kid")
	choreID, err := chores.Create(ctx, "Daily chore", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defID, err := defs.CreateRecurring(ctx, childID, choreID, 5, 0b1111111)
	if err != nil {
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
	// Resolve yesterday's instance (approved) so it should survive the cascade.
	if _, err := instances.MarkComplete(ctx, yList[0].ID); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	if err := instances.Decide(ctx, yList[0].ID, true, 0); err != nil {
		t.Fatalf("Decide (approve): %v", err)
	}

	tList, err := instances.ListForDate(ctx, today)
	if err != nil || len(tList) != 1 {
		t.Fatalf("ListForDate(today): list=%+v err=%v", tList, err)
	}
	// Today's instance is left 'incomplete' - the cascade should delete it.

	if err := chores.Deactivate(ctx, choreID); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}

	gotChore, err := chores.GetByID(ctx, choreID)
	if err != nil {
		t.Fatalf("GetByID chore: %v", err)
	}
	if gotChore.Active {
		t.Fatal("expected the catalog chore to be deactivated")
	}

	afterActive, err := defs.ListActive(ctx)
	if err != nil {
		t.Fatalf("ListActive defs: %v", err)
	}
	for _, d := range afterActive {
		if d.ID == defID {
			t.Fatalf("expected the chore definition to be deactivated too, still active: %+v", d)
		}
	}

	if _, err := instances.GetByID(ctx, tList[0].ID); err == nil {
		t.Fatal("expected today's unresolved instance to be deleted by the cascade")
	}

	approvedStillThere, err := instances.GetByID(ctx, yList[0].ID)
	if err != nil {
		t.Fatalf("expected yesterday's approved instance to survive the cascade, got err: %v", err)
	}
	if approvedStillThere.Status != StatusApproved {
		t.Fatalf("approved instance status changed unexpectedly: %+v", approvedStillThere)
	}
}

// TestCollapseChoreInstancesFallsBackToLatestUnresolvedWithoutToday and
// TestCollapseChoreInstancesFallsBackToLatestOverallWhenAllResolved exercise
// collapseChoreInstances directly (it's pure, package-private logic, no DB
// needed) to cover its two fallback branches - neither is reachable through
// ListActiveForDate's own SQL in the "all resolved, nothing due today" case,
// since a fully-resolved, non-today instance is excluded by that query
// entirely (see ListActiveForDate's WHERE clause), so it can never appear in
// the `instances` slice collapseChoreInstances is asked to group in practice.
// Calling the function directly is the only way to exercise that branch.
func TestCollapseChoreInstancesFallsBackToLatestUnresolvedWithoutToday(t *testing.T) {
	todayUTC := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	yesterday := todayUTC.AddDate(0, 0, -1)
	twoDaysAgo := todayUTC.AddDate(0, 0, -2)

	instances := []ChoreInstance{
		{ID: 1, ChoreDefinitionID: 10, ChildID: 1, DueDate: twoDaysAgo, Status: StatusIncomplete},
		{ID: 2, ChoreDefinitionID: 10, ChildID: 1, DueDate: yesterday, Status: StatusPendingApproval},
	}

	got := collapseChoreInstances(instances, todayUTC)
	if len(got) != 1 {
		t.Fatalf("collapseChoreInstances = %+v, want a single collapsed entry", got)
	}
	if got[0].ID != 2 {
		t.Fatalf("expected the latest unresolved instance (id 2) to be chosen as representative, got id %d", got[0].ID)
	}
	if !got[0].IsLate {
		t.Fatal("expected IsLate to be set given unresolved backlog before today")
	}
}

func TestCollapseChoreInstancesFallsBackToLatestOverallWhenAllResolved(t *testing.T) {
	todayUTC := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	yesterday := todayUTC.AddDate(0, 0, -1)
	twoDaysAgo := todayUTC.AddDate(0, 0, -2)

	instances := []ChoreInstance{
		{ID: 1, ChoreDefinitionID: 10, ChildID: 1, DueDate: twoDaysAgo, Status: StatusApproved},
		{ID: 2, ChoreDefinitionID: 10, ChildID: 1, DueDate: yesterday, Status: StatusApproved},
	}

	got := collapseChoreInstances(instances, todayUTC)
	if len(got) != 1 {
		t.Fatalf("collapseChoreInstances = %+v, want a single collapsed entry", got)
	}
	if got[0].ID != 2 {
		t.Fatalf("expected the most recent overall instance (id 2) to be chosen as representative, got id %d", got[0].ID)
	}
	if got[0].IsLate {
		t.Fatal("expected IsLate to be false when nothing is unresolved")
	}
}

// TestChoreQueriesReturnErrorOnCanceledContext covers the query/exec-error
// return branches shared across ChoreDefinitionStore/ChoreInstanceStore
// methods, otherwise unreachable without breaking the DB connection - see
// the equivalent calendar.go test for the same technique/rationale.
func TestChoreQueriesReturnErrorOnCanceledContext(t *testing.T) {
	conn := testutil.RequireDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	defs := &ChoreDefinitionStore{DB: conn}
	instances := &ChoreInstanceStore{DB: conn}

	if _, err := defs.ListActive(ctx); err == nil {
		t.Error("ChoreDefinitionStore.ListActive: expected error on canceled context")
	}
	if _, err := instances.ListForDate(ctx, time.Now()); err == nil {
		t.Error("ChoreInstanceStore.ListForDate: expected error on canceled context")
	}
	if _, err := instances.ListForWeek(ctx, time.Now()); err == nil {
		t.Error("ChoreInstanceStore.ListForWeek: expected error on canceled context")
	}
	if _, err := instances.ListActiveForDate(ctx, time.Now()); err == nil {
		t.Error("ChoreInstanceStore.ListActiveForDate: expected error on canceled context")
	}
	if _, err := instances.MarkComplete(ctx, 1); err == nil {
		t.Error("ChoreInstanceStore.MarkComplete: expected error on canceled context")
	}
	if err := instances.Decide(ctx, 1, true, 0); err == nil {
		t.Error("ChoreInstanceStore.Decide: expected error on canceled context")
	}
	if err := instances.ResetRejected(ctx, 1); err == nil {
		t.Error("ChoreInstanceStore.ResetRejected: expected error on canceled context")
	}
}
