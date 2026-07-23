package handlers

import (
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/models"
)

func TestSanitizeNextPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty defaults to /parent", "", "/parent"},
		{"valid path passes through", "/parent/report", "/parent/report"},
		{"absolute URL rejected", "https://evil.example.com/phish", "/parent"},
		{"protocol-relative URL rejected", "//evil.example.com/phish", "/parent"},
		{"missing leading slash rejected", "parent", "/parent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeNextPath(tt.in); got != tt.want {
				t.Errorf("sanitizeNextPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseIDParam(t *testing.T) {
	if _, err := parseInt("not-a-number"); err == nil {
		t.Error("expected an error parsing a non-numeric id")
	}
	got, err := parseInt("42")
	if err != nil || got != 42 {
		t.Errorf("parseInt(\"42\") = (%d, %v), want (42, nil)", got, err)
	}
}

func TestGroupEventsByDaySortsAndLabelsGroups(t *testing.T) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location())
	tomorrow := today.AddDate(0, 0, 1)
	dayAfter := today.AddDate(0, 0, 2)

	events := []models.Event{
		{Summary: "Day after event", StartsAt: dayAfter},
		{Summary: "Today event", StartsAt: today},
		{Summary: "Tomorrow event", StartsAt: tomorrow},
	}

	groups := groupEventsByDay(events)
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3", len(groups))
	}
	if groups[0].Label != "Today" {
		t.Errorf("groups[0].Label = %q, want Today", groups[0].Label)
	}
	if groups[1].Label != "Tomorrow" {
		t.Errorf("groups[1].Label = %q, want Tomorrow", groups[1].Label)
	}
	if groups[2].Label == "Today" || groups[2].Label == "Tomorrow" {
		t.Errorf("groups[2].Label = %q, want a weekday-formatted label", groups[2].Label)
	}
}

func TestGroupEventsByDayGroupsSameDayEventsTogether(t *testing.T) {
	now := time.Now()
	morning := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location())
	evening := time.Date(now.Year(), now.Month(), now.Day(), 20, 0, 0, 0, now.Location())

	groups := groupEventsByDay([]models.Event{
		{Summary: "Morning", StartsAt: morning},
		{Summary: "Evening", StartsAt: evening},
	})
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1 (same calendar day)", len(groups))
	}
	if len(groups[0].Events) != 2 {
		t.Fatalf("got %d events in the group, want 2", len(groups[0].Events))
	}
}

func TestGroupChoresByChildSortsByChildID(t *testing.T) {
	instances := []models.ChoreInstance{
		{ChildID: 2, ChildName: "Bob", ChildColor: "#EF4444", ChoreName: "Bob's chore"},
		{ChildID: 1, ChildName: "Alice", ChildColor: "#3B82F6", ChoreName: "Alice's chore 1"},
		{ChildID: 1, ChildName: "Alice", ChildColor: "#3B82F6", ChoreName: "Alice's chore 2"},
	}

	columns := groupChoresByChild(instances, nil)
	if len(columns) != 2 {
		t.Fatalf("got %d columns, want 2", len(columns))
	}
	if columns[0].Name != "Alice" || len(columns[0].Chores) != 2 {
		t.Errorf("columns[0] = %+v, want Alice with 2 chores", columns[0])
	}
	if columns[1].Name != "Bob" || len(columns[1].Chores) != 1 {
		t.Errorf("columns[1] = %+v, want Bob with 1 chore", columns[1])
	}
}

func TestSameDay(t *testing.T) {
	a := time.Date(2026, 8, 1, 23, 0, 0, 0, time.UTC)
	b := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	c := time.Date(2026, 8, 2, 1, 0, 0, 0, time.UTC)

	if !sameDay(a, b) {
		t.Error("expected same calendar day regardless of time-of-day")
	}
	if sameDay(a, c) {
		t.Error("expected different calendar days to not match")
	}
}

func TestStartOfWeek(t *testing.T) {
	wednesday := time.Date(2026, 8, 5, 14, 0, 0, 0, time.UTC)
	got := startOfWeek(wednesday)
	if got.Weekday() != time.Sunday {
		t.Errorf("startOfWeek weekday = %v, want Sunday", got.Weekday())
	}
	if got.Hour() != 0 || got.Minute() != 0 || got.Second() != 0 {
		t.Errorf("startOfWeek should be midnight, got %v", got)
	}
}

func TestNullString(t *testing.T) {
	if got := nullString(""); got.Valid {
		t.Errorf("nullString(\"\") = %+v, want invalid", got)
	}
	got := nullString("value")
	if !got.Valid || got.String != "value" {
		t.Errorf("nullString(\"value\") = %+v, want valid \"value\"", got)
	}
}
