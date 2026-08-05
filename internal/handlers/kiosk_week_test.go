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

package handlers

import (
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/weather"
)

func TestResolveGridRangeDefaultsWhenUnsetOrInvalid(t *testing.T) {
	tests := []struct {
		name               string
		mode, start, end   string
		wantStart, wantEnd int
		wantMode           string
	}{
		{"valid fixed range", "fixed", "6", "22", 6, 22, "fixed"},
		{"valid full24 range", "full24", "6", "22", 6, 22, "full24"},
		{"unparseable start falls back", "fixed", "not-a-number", "22", 6, 22, "fixed"},
		{"out-of-range hour falls back", "fixed", "6", "24", 6, 22, "fixed"},
		{"end before start falls back", "fixed", "20", "8", 6, 22, "fixed"},
		{"end equal start falls back", "fixed", "10", "10", 6, 22, "fixed"},
		{"unknown mode defaults to fixed", "bogus", "6", "22", 6, 22, "fixed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveGridRange(tt.mode, tt.start, tt.end)
			if got.Mode != tt.wantMode || got.StartHour != tt.wantStart || got.EndHour != tt.wantEnd {
				t.Errorf("resolveGridRange(%q,%q,%q) = %+v, want Mode=%s Start=%d End=%d",
					tt.mode, tt.start, tt.end, got, tt.wantMode, tt.wantStart, tt.wantEnd)
			}
		})
	}
}

func TestGroupEventsByDayKeyGroupsByLocalDate(t *testing.T) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location())
	tomorrow := today.AddDate(0, 0, 1)

	byDay := groupEventsByDayKey([]models.Event{
		{Summary: "Today A", StartsAt: today},
		{Summary: "Today B", StartsAt: today.Add(time.Hour)},
		{Summary: "Tomorrow", StartsAt: tomorrow},
	})

	todayKey := today.Format("2006-01-02")
	tomorrowKey := tomorrow.Format("2006-01-02")
	if len(byDay[todayKey]) != 2 {
		t.Errorf("got %d events for today, want 2", len(byDay[todayKey]))
	}
	if len(byDay[tomorrowKey]) != 1 {
		t.Errorf("got %d events for tomorrow, want 1", len(byDay[tomorrowKey]))
	}
}

func TestPositionEventsOnGridComputesCorrectPercentages(t *testing.T) {
	grid := weekGridConfig{Mode: kioskGridModeFixed, StartHour: 6, EndHour: 22} // 16h span
	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	events := []models.Event{
		{StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(10 * time.Hour)}, // 9am-10am
	}

	got := positionEventsOnGrid(events, grid)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	// 9am is 3h (180min) after the 6am grid start, within a 16h (960min) span.
	wantTop := 180.0 / 960.0 * 100
	wantHeight := 60.0 / 960.0 * 100
	if diff := got[0].TopPct - wantTop; diff > 0.01 || diff < -0.01 {
		t.Errorf("TopPct = %v, want %v", got[0].TopPct, wantTop)
	}
	if diff := got[0].HeightPct - wantHeight; diff > 0.01 || diff < -0.01 {
		t.Errorf("HeightPct = %v, want %v", got[0].HeightPct, wantHeight)
	}
	if got[0].Clipped {
		t.Error("event fully inside the grid range should not be Clipped")
	}
}

func TestPositionEventsOnGridFixedModeClipsOutOfRangeEvents(t *testing.T) {
	grid := weekGridConfig{Mode: kioskGridModeFixed, StartHour: 6, EndHour: 22}
	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	events := []models.Event{
		{StartsAt: day.Add(5 * time.Hour), EndsAt: day.Add(7 * time.Hour)}, // starts before grid.StartHour
	}

	got := positionEventsOnGrid(events, grid)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if !got[0].Clipped {
		t.Error("event starting before the grid range should be Clipped in fixed mode")
	}
	if got[0].TopPct != 0 {
		t.Errorf("TopPct = %v, want 0 (clamped to the grid edge)", got[0].TopPct)
	}
}

func TestPositionEventsOnGridFull24ModeNeverClips(t *testing.T) {
	grid := weekGridConfig{Mode: kioskGridModeFull24}
	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	events := []models.Event{
		{StartsAt: day.Add(1 * time.Hour), EndsAt: day.Add(2 * time.Hour)},
		{StartsAt: day.Add(23 * time.Hour), EndsAt: day.Add(24 * time.Hour)},
	}

	got := positionEventsOnGrid(events, grid)
	for i, e := range got {
		if e.Clipped {
			t.Errorf("event %d should never be Clipped in full24 mode", i)
		}
	}
}

func TestPositionEventsOnGridAllDayEventsUnpositioned(t *testing.T) {
	grid := weekGridConfig{Mode: kioskGridModeFixed, StartHour: 6, EndHour: 22}
	got := positionEventsOnGrid([]models.Event{{Summary: "All day", AllDay: true}}, grid)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].TopPct != 0 || got[0].HeightPct != 0 || got[0].Clipped {
		t.Errorf("all-day event = %+v, want zero Top/HeightPct and not Clipped", got[0])
	}
}

func TestPositionEventsOnGridMinimumHeightForZeroDurationEvents(t *testing.T) {
	grid := weekGridConfig{Mode: kioskGridModeFixed, StartHour: 6, EndHour: 22}
	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	events := []models.Event{
		{StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(9 * time.Hour)}, // zero duration
	}

	got := positionEventsOnGrid(events, grid)
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1", len(got))
	}
	if got[0].HeightPct < minEventHeightPct {
		t.Errorf("HeightPct = %v, want at least the minimum %v", got[0].HeightPct, minEventHeightPct)
	}
}

func TestPositionEventsOnGridSingleEventUsesFullWidth(t *testing.T) {
	grid := weekGridConfig{Mode: kioskGridModeFixed, StartHour: 6, EndHour: 22}
	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	events := []models.Event{
		{StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(10 * time.Hour)},
	}

	got := positionEventsOnGrid(events, grid)
	if got[0].LeftPct != weekEventTrackLeftPct || got[0].WidthPct != weekEventTrackWidthPct {
		t.Errorf("single non-overlapping event: LeftPct/WidthPct = %v/%v, want %v/%v",
			got[0].LeftPct, got[0].WidthPct, weekEventTrackLeftPct, weekEventTrackWidthPct)
	}
}

func TestPositionEventsOnGridOverlappingEventsSplitColumns(t *testing.T) {
	grid := weekGridConfig{Mode: kioskGridModeFixed, StartHour: 6, EndHour: 22}
	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	events := []models.Event{
		{Summary: "Band Camp", StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(10 * time.Hour)},
		{Summary: "Empowered Relief", StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(10 * time.Hour)},
	}

	got := positionEventsOnGrid(events, grid)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	wantWidth := weekEventTrackWidthPct/2 - weekEventColumnGutter/2
	for i, e := range got {
		if diff := e.WidthPct - wantWidth; diff > 0.01 || diff < -0.01 {
			t.Errorf("event %d WidthPct = %v, want %v", i, e.WidthPct, wantWidth)
		}
	}
	if got[0].LeftPct == got[1].LeftPct {
		t.Errorf("overlapping events should not share the same LeftPct, both got %v", got[0].LeftPct)
	}
	if got[0].LeftPct != weekEventTrackLeftPct {
		t.Errorf("first event LeftPct = %v, want %v (left margin)", got[0].LeftPct, weekEventTrackLeftPct)
	}
}

func TestPositionEventsOnGridThreeWayOverlapUsesThreeColumns(t *testing.T) {
	grid := weekGridConfig{Mode: kioskGridModeFixed, StartHour: 6, EndHour: 22}
	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	events := []models.Event{
		{StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(10 * time.Hour)},
		{StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(10 * time.Hour)},
		{StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(10 * time.Hour)},
	}

	got := positionEventsOnGrid(events, grid)
	seen := map[float64]bool{}
	for _, e := range got {
		seen[e.LeftPct] = true
		wantWidth := weekEventTrackWidthPct/3 - weekEventColumnGutter*2/3
		if diff := e.WidthPct - wantWidth; diff > 0.01 || diff < -0.01 {
			t.Errorf("WidthPct = %v, want %v", e.WidthPct, wantWidth)
		}
	}
	if len(seen) != 3 {
		t.Errorf("got %d distinct LeftPct values, want 3", len(seen))
	}
}

func TestPositionEventsOnGridBackToBackEventsDoNotSplitColumns(t *testing.T) {
	grid := weekGridConfig{Mode: kioskGridModeFixed, StartHour: 6, EndHour: 22}
	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	events := []models.Event{
		{StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(10 * time.Hour)},
		{StartsAt: day.Add(10 * time.Hour), EndsAt: day.Add(11 * time.Hour)}, // starts exactly when the first ends
	}

	got := positionEventsOnGrid(events, grid)
	for i, e := range got {
		if e.WidthPct != weekEventTrackWidthPct || e.LeftPct != weekEventTrackLeftPct {
			t.Errorf("back-to-back event %d = %+v, want full-width single column", i, e)
		}
	}
}

func TestPositionEventsOnGridUnrelatedClustersLayoutIndependently(t *testing.T) {
	grid := weekGridConfig{Mode: kioskGridModeFixed, StartHour: 6, EndHour: 22}
	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	events := []models.Event{
		{StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(10 * time.Hour)},
		{StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(10 * time.Hour)},
		{StartsAt: day.Add(15 * time.Hour), EndsAt: day.Add(16 * time.Hour)}, // unrelated, later, non-overlapping
	}

	got := positionEventsOnGrid(events, grid)
	afternoon := got[2]
	if afternoon.WidthPct != weekEventTrackWidthPct || afternoon.LeftPct != weekEventTrackLeftPct {
		t.Errorf("unrelated later event = %+v, want full-width single column despite earlier 2-way overlap", afternoon)
	}
}

// TestPositionEventsOnGridThreeWayMutualOverlapCannotExpand is a direct
// regression test for the reported "Band Camp / Empowered Relief / Jamie
// work" screenshot: all three events genuinely overlap each other's time
// range at some point, so none of them should expand past its own column -
// each is stuck at a bare 1/3 width, which is the correct outcome here (see
// the sibling test below for a case where expansion actually applies).
func TestPositionEventsOnGridThreeWayMutualOverlapCannotExpand(t *testing.T) {
	grid := weekGridConfig{Mode: kioskGridModeFixed, StartHour: 6, EndHour: 22}
	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	events := []models.Event{
		{Summary: "Band Camp", StartsAt: day.Add(8 * time.Hour), EndsAt: day.Add(15 * time.Hour)},       // 8am-3pm, col 0
		{Summary: "Empowered Relief", StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(10 * time.Hour)}, // 9am-10am, col 1 (overlaps Band Camp only)
		{Summary: "Jamie work", StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(17 * time.Hour)},       // 9am-5pm, overlaps both - forced into col 2
	}

	got := positionEventsOnGrid(events, grid)
	bandCamp, empoweredRelief, jamieWork := got[0], got[1], got[2]
	unitWidth := weekEventTrackWidthPct/3 - weekEventColumnGutter*2/3
	rightEdge := weekEventTrackLeftPct + weekEventTrackWidthPct

	// Jamie work is in the last (3rd) column with nothing further right -
	// it should reach the track's right edge, same as before this change.
	if diff := (jamieWork.LeftPct + jamieWork.WidthPct) - rightEdge; diff > 0.01 || diff < -0.01 {
		t.Errorf("Jamie work should extend to the track's right edge (%v), got left+width = %v", rightEdge, jamieWork.LeftPct+jamieWork.WidthPct)
	}

	// Empowered Relief cannot expand right - Jamie work occupies column 2
	// for the entirety of Empowered Relief's 9-10am span.
	if diff := empoweredRelief.WidthPct - unitWidth; diff > 0.01 || diff < -0.01 {
		t.Errorf("Empowered Relief WidthPct = %v, want single-column width %v (blocked by Jamie work)", empoweredRelief.WidthPct, unitWidth)
	}

	// Band Camp cannot expand right either - both later columns are
	// occupied by something overlapping part of its 8am-3pm span.
	if diff := bandCamp.WidthPct - unitWidth; diff > 0.01 || diff < -0.01 {
		t.Errorf("Band Camp WidthPct = %v, want single-column width %v (blocked by Empowered Relief/Jamie work)", bandCamp.WidthPct, unitWidth)
	}
}

// TestPositionEventsOnGridExpandsIntoColumnFreedByAnEarlierEvent covers the
// case where expansion should actually happen: a later event reuses a
// column an earlier, unrelated event has already vacated, freeing a
// further column for it to expand into.
//
// Column assignment for same-start events is greedy-by-ascending-end (C
// ends soonest, so it's placed first): C -> col 0, B -> col 1 (can't reuse
// col 0, C hasn't ended yet), A -> col 2 (can't reuse col 0 or col 1
// either). D starts at 9:30, exactly when C's col 0 slot frees up, so D
// reuses col 0. From there, col 1 (B, which ended at 9:30) no longer
// overlaps D's 9:30-10:00 span, so D should expand into col 1 - but col 2
// (A, still running until noon) does overlap D, so the expansion must stop
// there rather than reaching the track's right edge.
func TestPositionEventsOnGridExpandsIntoColumnFreedByAnEarlierEvent(t *testing.T) {
	grid := weekGridConfig{Mode: kioskGridModeFixed, StartHour: 6, EndHour: 22}
	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	events := []models.Event{
		{Summary: "A", StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(12 * time.Hour)},
		{Summary: "B", StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(9*time.Hour + 30*time.Minute)},
		{Summary: "C", StartsAt: day.Add(9 * time.Hour), EndsAt: day.Add(9*time.Hour + 15*time.Minute)},
		{Summary: "D", StartsAt: day.Add(9*time.Hour + 30*time.Minute), EndsAt: day.Add(10 * time.Hour)},
	}

	got := positionEventsOnGrid(events, grid)
	unitWidth := weekEventTrackWidthPct/3 - weekEventColumnGutter*2/3

	d := got[3]
	// D spans columns 0-1 (2 columns), not all 3 - column 2 (A) still
	// blocks it from reaching the track's right edge.
	wantWidth := unitWidth*2 + weekEventColumnGutter
	if diff := d.WidthPct - wantWidth; diff > 0.01 || diff < -0.01 {
		t.Errorf("D WidthPct = %v, want %v (expanded across 2 columns, blocked by A in column 2)", d.WidthPct, wantWidth)
	}
	if diff := d.LeftPct - weekEventTrackLeftPct; diff > 0.01 || diff < -0.01 {
		t.Errorf("D LeftPct = %v, want %v (reuses column 0, freed by C)", d.LeftPct, weekEventTrackLeftPct)
	}
	rightEdge := weekEventTrackLeftPct + weekEventTrackWidthPct
	if diff := (d.LeftPct + d.WidthPct) - rightEdge; diff > -0.01 {
		t.Errorf("D should NOT reach the track's right edge (%v) - column 2 (A) is still occupied, got left+width = %v", rightEdge, d.LeftPct+d.WidthPct)
	}
}

func TestHourLabelsForGridFixedAndFull24(t *testing.T) {
	fixed := hourLabelsForGrid(weekGridConfig{Mode: kioskGridModeFixed, StartHour: 6, EndHour: 22})
	if len(fixed) != 16 {
		t.Errorf("fixed grid: got %d labels, want 16", len(fixed))
	}
	if fixed[0].Label != "6 AM" || fixed[0].TopPct != 0 {
		t.Errorf("fixed grid: first label = %+v, want Label=%q TopPct=0", fixed[0], "6 AM")
	}
	// The 9 AM label (index 3, 3h after the 6am start) should sit at 3/16 of
	// the way down the 16h grid - the same coordinate space
	// positionEventsOnGrid uses for a 9am event's TopPct.
	wantNoonPct := 3.0 / 16.0 * 100
	if diff := fixed[3].TopPct - wantNoonPct; diff > 0.01 || diff < -0.01 {
		t.Errorf("fixed grid: label[3] (%q) TopPct = %v, want %v", fixed[3].Label, fixed[3].TopPct, wantNoonPct)
	}

	full24 := hourLabelsForGrid(weekGridConfig{Mode: kioskGridModeFull24})
	if len(full24) != 24 {
		t.Errorf("full24 grid: got %d labels, want 24", len(full24))
	}
	if full24[0].Label != "12 AM" || full24[0].TopPct != 0 {
		t.Errorf("full24 grid: first label = %+v, want Label=%q TopPct=0", full24[0], "12 AM")
	}
}

// TestHourLabelTopPctMatchesEventTopPctForSameHour is a direct regression
// test for the now-line/hour-axis misalignment bug: an event starting
// exactly on the hour must land at the same TopPct as that hour's axis
// label, since both are meant to share one coordinate space.
func TestHourLabelTopPctMatchesEventTopPctForSameHour(t *testing.T) {
	grid := weekGridConfig{Mode: kioskGridModeFixed, StartHour: 6, EndHour: 22}
	labels := hourLabelsForGrid(grid)

	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local)
	events := positionEventsOnGrid([]models.Event{
		{StartsAt: day.Add(15 * time.Hour), EndsAt: day.Add(16 * time.Hour)}, // 3 PM
	}, grid)

	var threePMLabel *weekHourLabel
	for i := range labels {
		if labels[i].Label == "3 PM" {
			threePMLabel = &labels[i]
		}
	}
	if threePMLabel == nil {
		t.Fatal("expected a \"3 PM\" label in the fixed 6am-10pm grid")
	}
	if diff := events[0].TopPct - threePMLabel.TopPct; diff > 0.01 || diff < -0.01 {
		t.Errorf("3 PM event TopPct = %v, want it to match the 3 PM axis label's TopPct = %v", events[0].TopPct, threePMLabel.TopPct)
	}
}

func TestForecastDailyByDayKeyEmptyWhenNoForecastCached(t *testing.T) {
	got := forecastDailyByDayKey(&weather.Cache{})
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0 for an empty cache", len(got))
	}
}

func TestForecastDailyByDayKeyKeysByLocalDate(t *testing.T) {
	cache := &weather.Cache{}
	day1 := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	cache.Set(&weather.Forecast{
		Daily: []weather.DayPoint{
			{Date: day1, Code: 1, TempMax: 88},
			{Date: day2, Code: 61, TempMax: 72},
		},
	})

	got := forecastDailyByDayKey(cache)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if dp := got["2026-07-28"]; dp.TempMax != 88 {
		t.Errorf("2026-07-28 TempMax = %v, want 88", dp.TempMax)
	}
	if dp := got["2026-07-29"]; dp.TempMax != 72 {
		t.Errorf("2026-07-29 TempMax = %v, want 72", dp.TempMax)
	}
}

func TestParseHourValidatesRange(t *testing.T) {
	if _, ok := parseHour("23"); !ok {
		t.Error("parseHour(\"23\") should be valid")
	}
	if _, ok := parseHour("24"); ok {
		t.Error("parseHour(\"24\") should be invalid (out of range)")
	}
	if _, ok := parseHour("-1"); ok {
		t.Error("parseHour(\"-1\") should be invalid (out of range)")
	}
	if _, ok := parseHour("abc"); ok {
		t.Error("parseHour(\"abc\") should be invalid (unparseable)")
	}
}
