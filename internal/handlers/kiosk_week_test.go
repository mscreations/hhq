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
