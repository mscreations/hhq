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

package report

import (
	"bytes"
	"database/sql"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/models"
)

func TestIsLate(t *testing.T) {
	dueDate := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		completedAt time.Time
		want        bool
	}{
		{"completed earlier same day", time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC), false},
		{"completed right at end of day", time.Date(2026, 8, 1, 23, 59, 59, 0, time.UTC), false},
		{"completed a nanosecond past end of day", time.Date(2026, 8, 1, 23, 59, 59, 1, time.UTC), true},
		{"completed the next day", time.Date(2026, 8, 2, 0, 0, 1, 0, time.UTC), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLate(tt.completedAt, dueDate); got != tt.want {
				t.Errorf("isLate(%v, %v) = %v, want %v", tt.completedAt, dueDate, got, tt.want)
			}
		})
	}
}

func TestSummarizeBucketsByStatus(t *testing.T) {
	dueDate := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	onTimeCompletion := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	lateCompletion := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)

	instances := []models.ChoreInstance{
		{ChildName: "Alice", ChoreName: "On time chore", Points: 5, Status: models.StatusApproved, DueDate: dueDate, CompletedAt: sql.NullTime{Time: onTimeCompletion, Valid: true}},
		{ChildName: "Alice", ChoreName: "Late chore", Points: 3, Status: models.StatusApproved, DueDate: dueDate, CompletedAt: sql.NullTime{Time: lateCompletion, Valid: true}},
		{ChildName: "Alice", ChoreName: "Rejected chore", Points: 2, Status: models.StatusRejected, DueDate: dueDate},
		{ChildName: "Alice", ChoreName: "Not done", Points: 1, Status: models.StatusIncomplete, DueDate: dueDate},
		{ChildName: "Alice", ChoreName: "Still pending", Points: 4, Status: models.StatusPendingApproval, DueDate: dueDate},
		{ChildName: "Bob", ChoreName: "Bob's chore", Points: 10, Status: models.StatusApproved, DueDate: dueDate, CompletedAt: sql.NullTime{Time: onTimeCompletion, Valid: true}},
	}

	summaries := summarize(instances)

	if len(summaries) != 2 {
		t.Fatalf("got %d children, want 2", len(summaries))
	}

	alice := summaries["Alice"]
	if alice == nil {
		t.Fatal("expected a summary for Alice")
	}
	if len(alice.OnTime) != 1 || alice.OnTime[0].ChoreName != "On time chore" {
		t.Errorf("OnTime = %+v", alice.OnTime)
	}
	if len(alice.Late) != 1 || alice.Late[0].ChoreName != "Late chore" {
		t.Errorf("Late = %+v", alice.Late)
	}
	if len(alice.Rejected) != 1 || alice.Rejected[0].ChoreName != "Rejected chore" {
		t.Errorf("Rejected = %+v", alice.Rejected)
	}
	// Both "Not done" (incomplete) and "Still pending" (pending_approval)
	// bucket into Incomplete for reporting purposes.
	if len(alice.Incomplete) != 2 {
		t.Errorf("Incomplete = %+v, want 2 entries", alice.Incomplete)
	}
	// Points only accrue for approved chores: 5 (on time) + 3 (late) = 8.
	if alice.Points != 8 {
		t.Errorf("Alice.Points = %d, want 8", alice.Points)
	}

	bob := summaries["Bob"]
	if bob == nil || bob.Points != 10 {
		t.Fatalf("Bob summary = %+v, want 10 points", bob)
	}
}

func TestSummarizeApprovedWithoutCompletedAtCountsAsOnTime(t *testing.T) {
	// Defensive case: an approved instance with no CompletedAt (shouldn't
	// happen in practice since MarkComplete always sets it, but the isLate
	// check is guarded by CompletedAt.Valid) should not panic and should
	// fall into OnTime rather than Late.
	instances := []models.ChoreInstance{
		{ChildName: "Alice", ChoreName: "Weird chore", Points: 1, Status: models.StatusApproved, DueDate: time.Now()},
	}
	summaries := summarize(instances)
	alice := summaries["Alice"]
	if len(alice.OnTime) != 1 || len(alice.Late) != 0 {
		t.Fatalf("expected approved-without-CompletedAt to count as OnTime, got OnTime=%+v Late=%+v", alice.OnTime, alice.Late)
	}
}

func TestBuildWeeklyPDFProducesValidPDFBytes(t *testing.T) {
	weekStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	instances := []models.ChoreInstance{
		{
			ChildName: "Alice", ChoreName: "Take out trash", Points: 5,
			Status: models.StatusApproved, DueDate: weekStart,
			CompletedAt: sql.NullTime{Time: weekStart.Add(time.Hour), Valid: true},
		},
		{
			ChildName: "Alice", ChoreName: "Clean room", Points: 3,
			Status: models.StatusRejected, DueDate: weekStart.AddDate(0, 0, 1),
		},
	}

	data, err := BuildWeeklyPDF(weekStart, instances)
	if err != nil {
		t.Fatalf("BuildWeeklyPDF: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty PDF bytes")
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		n := len(data)
		if n > 20 {
			n = 20
		}
		t.Fatalf("output does not start with a PDF header: %q", data[:n])
	}
	if !bytes.HasSuffix(bytes.TrimRight(data, "\n\r"), []byte("%%EOF")) {
		t.Fatal("output does not end with a PDF trailer")
	}
}

func TestBuildWeeklyPDFWithNoInstancesStillProducesValidPDF(t *testing.T) {
	data, err := BuildWeeklyPDF(time.Now(), nil)
	if err != nil {
		t.Fatalf("BuildWeeklyPDF with no instances: %v", err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Fatal("expected a valid PDF header even with zero chore instances")
	}
}
