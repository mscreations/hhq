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

// Package report builds the weekly chore PDF report: per child, which chores
// were done on time, which were done late, and which weren't done at all,
// plus total points earned.
package report

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"github.com/jung-kurt/gofpdf"

	"github.com/mscreations/hhq/internal/models"
)

type childSummary struct {
	Name       string
	OnTime     []models.ChoreInstance
	Late       []models.ChoreInstance
	Incomplete []models.ChoreInstance
	Rejected   []models.ChoreInstance
	Points     int
}

// BuildWeeklyPDF generates a PDF summarizing the given week's chore instances,
// grouped by child. weekStart should be the first day of the week used when the
// instances were queried (see models.ChoreInstanceStore.ListForWeek).
func BuildWeeklyPDF(weekStart time.Time, instances []models.ChoreInstance) ([]byte, error) {
	summaries := summarize(instances)

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Helvetica", "B", 18)
	pdf.CellFormat(0, 10, "Weekly Chore Report", "", 1, "C", false, 0, "")
	pdf.SetFont("Helvetica", "", 11)
	weekEnd := weekStart.AddDate(0, 0, 6)
	pdf.CellFormat(0, 8, fmt.Sprintf("%s - %s", weekStart.Format("Jan 2"), weekEnd.Format("Jan 2, 2006")), "", 1, "C", false, 0, "")
	pdf.Ln(6)

	names := make([]string, 0, len(summaries))
	for name := range summaries {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		cs := summaries[name]
		pdf.SetFont("Helvetica", "B", 14)
		pdf.SetFillColor(240, 240, 240)
		pdf.CellFormat(0, 10, fmt.Sprintf("%s  -  %d points earned", cs.Name, cs.Points), "", 1, "L", true, 0, "")
		pdf.Ln(2)

		writeSection(pdf, "Completed On Time", cs.OnTime, [3]int{34, 197, 94})
		writeSection(pdf, "Completed Late", cs.Late, [3]int{234, 179, 8})
		writeSection(pdf, "Rejected", cs.Rejected, [3]int{239, 68, 68})
		writeSection(pdf, "Not Completed", cs.Incomplete, [3]int{156, 163, 175})

		pdf.Ln(6)
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("rendering pdf: %w", err)
	}
	return buf.Bytes(), nil
}

func writeSection(pdf *gofpdf.Fpdf, title string, items []models.ChoreInstance, rgb [3]int) {
	if len(items) == 0 {
		return
	}
	pdf.SetFont("Helvetica", "B", 11)
	pdf.SetTextColor(rgb[0], rgb[1], rgb[2])
	pdf.CellFormat(0, 7, title, "", 1, "L", false, 0, "")
	pdf.SetTextColor(0, 0, 0)
	pdf.SetFont("Helvetica", "", 10)
	for _, item := range items {
		line := fmt.Sprintf("  - %s (%s) - %d pts", item.ChoreName, item.DueDate.Format("Mon Jan 2"), item.Points)
		pdf.CellFormat(0, 6, line, "", 1, "L", false, 0, "")
	}
	pdf.Ln(1)
}

func summarize(instances []models.ChoreInstance) map[string]*childSummary {
	out := map[string]*childSummary{}
	for _, inst := range instances {
		cs, ok := out[inst.ChildName]
		if !ok {
			cs = &childSummary{Name: inst.ChildName}
			out[inst.ChildName] = cs
		}

		switch inst.Status {
		case models.StatusApproved:
			cs.Points += inst.Points
			if inst.CompletedAt.Valid && isLate(inst.CompletedAt.Time, inst.DueDate) {
				cs.Late = append(cs.Late, inst)
			} else {
				cs.OnTime = append(cs.OnTime, inst)
			}
		case models.StatusRejected:
			cs.Rejected = append(cs.Rejected, inst)
		case models.StatusPendingApproval:
			// Treat as not-yet-decided for reporting purposes until a parent acts.
			cs.Incomplete = append(cs.Incomplete, inst)
		default: // incomplete
			cs.Incomplete = append(cs.Incomplete, inst)
		}
	}
	return out
}

// isLate considers a chore "late" if it was completed after the end of its due date.
func isLate(completedAt, dueDate time.Time) bool {
	endOfDueDate := time.Date(dueDate.Year(), dueDate.Month(), dueDate.Day(), 23, 59, 59, 0, dueDate.Location())
	return completedAt.After(endOfDueDate)
}
