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
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/weather"
)

// Settings keys for the 5-day calendar view. All are plain scalars stored in
// the existing generic hhq_settings key/value table (see models.SettingsStore)
// - no schema change was needed for this feature.
const (
	settingKioskLayout        = "kiosk_layout"
	settingKioskGridMode      = "kiosk_grid_mode"
	settingKioskGridStartHour = "kiosk_grid_start_hour"
	settingKioskGridEndHour   = "kiosk_grid_end_hour"

	kioskLayoutClassic = "classic"
	kioskLayoutWeekly  = "weekly"

	kioskGridModeFixed  = "fixed"
	kioskGridModeFull24 = "full24"

	defaultKioskGridStartHour = 6
	defaultKioskGridEndHour   = 22

	weekViewDays = 5

	// kioskWeekViewQueryValue is the "?view=week&day=..." query param value
	// a chore <li> tapped from the 5-day view appends to its POST
	// /kiosk/chores/{id}/complete (see kiosk/_week_day_chores.html), so
	// KioskCompleteChore knows to re-render just that one day's chore column
	// instead of the classic view's kiosk/_chores. Deliberately distinct
	// from kioskLayoutWeekly ("weekly") - this identifies which fragment to
	// render, not which layout is the default.
	kioskWeekViewQueryValue = "week"
)

// weekDay is one day column in the 5-day view: that day's chores (flat, all
// children mixed, colored per child - see kiosk/_week_day_chores.html) pinned
// above a time-grid of that day's events.
type weekDay struct {
	Label   string // "Today"/"Tomorrow"/"Monday, Jan 2" - reuses dayLabel()
	Date    time.Time
	DateKey string // "2006-01-02", used to build unique per-column DOM ids
	Chores  []models.ChoreInstance
	Events  []weekEvent
	IsToday bool
	// HasForecast/WeatherIcon/WeatherHighTemp back the day heading's small
	// forecast icon+high-temp shown for every day except Today (see
	// kiosk/_week.html) - populated from the same in-memory weather.Cache
	// the header widget/weather page already read, never a fresh API call.
	HasForecast     bool
	WeatherIcon     string
	WeatherHighTemp float64
}

// weekEvent wraps a models.Event with server-computed vertical grid
// positioning (percentages of the configured grid range), so the template
// never has to do time arithmetic.
type weekEvent struct {
	models.Event
	TopPct    float64 // 0-100, clipped to the grid range
	HeightPct float64 // 0-100, clipped to the grid range
	// Clipped is true when the event's real start/end falls outside the
	// configured fixed-hours grid range and had to be compressed to the grid
	// edge to stay visible (never true in full24 mode, since the range is
	// always the whole day there).
	Clipped bool
}

// weekGridConfig is the resolved (settings-or-default) time-grid range for
// the 5-day view.
type weekGridConfig struct {
	Mode      string // "fixed" | "full24"
	StartHour int    // 0-23, only meaningful when Mode == "fixed"
	EndHour   int    // 0-23, only meaningful when Mode == "fixed"
}

// weekHourLabel is one label on the shared hour axis, with its vertical
// position pre-computed as a percentage - the same coordinate space
// positionEventsOnGrid uses for event TopPct/HeightPct - so the axis and
// every day's time grid are guaranteed to agree on where a given hour sits,
// rather than relying on CSS to eyeball the alignment.
type weekHourLabel struct {
	Label  string
	TopPct float64
}

type kioskWeekViewData struct {
	AppTitle       string
	Days           []weekDay
	Grid           weekGridConfig
	HourLabels     []weekHourLabel
	ParentLoggedIn bool
	Weather        weatherViewData
	Plugins        []pluginNavItem
}

// KioskFragmentWeek backs the 5-day view's nav button (either "Home" or the
// "Calendars"/"Agenda" alternate, depending on the kiosk_layout setting - see
// kiosk/index.html).
func (a *App) KioskFragmentWeek(w http.ResponseWriter, r *http.Request) {
	data, err := a.buildKioskWeekViewData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderFragment(w, "kiosk/_week", data)
}

func (a *App) buildKioskWeekViewData(r *http.Request) (*kioskWeekViewData, error) {
	ctx := r.Context()
	now := time.Now()

	grid, err := a.loadWeekGridConfig(ctx)
	if err != nil {
		return nil, err
	}

	appTitle, err := a.Settings.Get(ctx, "app_title", a.Cfg.AppTitle)
	if err != nil {
		return nil, err
	}

	// A single window query (not one ListInWindow call per day) covers all 5
	// days at once. weekViewDays is a literal, intentionally decoupled from
	// a.Cfg.CalendarWindowDays, which still drives the classic view's
	// separate "Upcoming" panel.
	windowEvents, err := a.Events.ListInWindow(ctx, weekViewDays)
	if err != nil {
		return nil, err
	}
	eventsByDay := groupEventsByDayKey(windowEvents)
	forecastByDay := forecastDailyByDayKey(a.Weather)

	days := make([]weekDay, 0, weekViewDays)
	for i := 0; i < weekViewDays; i++ {
		date := now.AddDate(0, 0, i)
		if err := a.ChoreInstances.EnsureForDate(ctx, date); err != nil {
			return nil, err
		}
		// ListForDate (not ListActiveForDate) - backlog-collapsing is a
		// "today" concept and doesn't fit a 5-day span; overdue backlog
		// chores stay visible only via the classic view.
		chores, err := a.ChoreInstances.ListForDate(ctx, date)
		if err != nil {
			return nil, err
		}
		key := date.Format("2006-01-02")
		isToday := sameDay(date, now)

		day := weekDay{
			Label:   dayLabel(date),
			Date:    date,
			DateKey: key,
			Chores:  chores,
			Events:  positionEventsOnGrid(eventsByDay[key], grid),
			IsToday: isToday,
		}
		// The forecast icon/high-temp is shown for every day except Today
		// (per the user's request - Today already has the header's own
		// current-conditions widget, so repeating it on the heading would be
		// redundant).
		if !isToday {
			if dp, ok := forecastByDay[key]; ok {
				code := weather.DescribeCode(dp.Code)
				day.HasForecast = true
				day.WeatherIcon = code.Icon
				day.WeatherHighTemp = dp.TempMax
			}
		}
		days = append(days, day)
	}

	user := auth.UserFromContext(ctx)
	return &kioskWeekViewData{
		AppTitle:       appTitle,
		Days:           days,
		Grid:           grid,
		HourLabels:     hourLabelsForGrid(grid),
		ParentLoggedIn: user != nil && user.Role == models.RoleParent,
		Weather:        a.currentWeatherViewData(),
		Plugins:        a.buildPluginNavItems(ctx),
	}, nil
}

// loadWeekGridConfig reads the parent-configurable time-grid range, falling
// back to a fixed 6am-10pm waking-hours window if unset or invalid.
func (a *App) loadWeekGridConfig(ctx context.Context) (weekGridConfig, error) {
	mode, err := a.Settings.Get(ctx, settingKioskGridMode, kioskGridModeFixed)
	if err != nil {
		return weekGridConfig{}, err
	}
	if mode != kioskGridModeFull24 {
		mode = kioskGridModeFixed
	}

	startHourStr, err := a.Settings.Get(ctx, settingKioskGridStartHour, strconv.Itoa(defaultKioskGridStartHour))
	if err != nil {
		return weekGridConfig{}, err
	}
	endHourStr, err := a.Settings.Get(ctx, settingKioskGridEndHour, strconv.Itoa(defaultKioskGridEndHour))
	if err != nil {
		return weekGridConfig{}, err
	}

	return resolveGridRange(mode, startHourStr, endHourStr), nil
}

// resolveGridRange validates and assembles a weekGridConfig from raw
// setting strings, falling back to the 6am-10pm default range whenever the
// configured start/end hours are missing, unparseable, or non-increasing -
// kept as a pure function (no DB access) so it's directly unit-testable,
// separate from loadWeekGridConfig's Settings.Get calls.
func resolveGridRange(mode, startHourStr, endHourStr string) weekGridConfig {
	if mode != kioskGridModeFull24 {
		mode = kioskGridModeFixed
	}
	startHour, startOK := parseHour(startHourStr)
	endHour, endOK := parseHour(endHourStr)
	if !startOK || !endOK || endHour <= startHour {
		startHour, endHour = defaultKioskGridStartHour, defaultKioskGridEndHour
	}
	return weekGridConfig{Mode: mode, StartHour: startHour, EndHour: endHour}
}

func parseHour(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 23 {
		return 0, false
	}
	return n, true
}

// groupEventsByDayKey groups events by their "2006-01-02" local-date key -
// a lighter variant of groupEventsByDay (kiosk.go) that returns a lookup map
// instead of an ordered []dayGroup slice, since the 5-day view already knows
// which days it wants (today..today+4) and just needs each day's events.
func groupEventsByDayKey(events []models.Event) map[string][]models.Event {
	out := make(map[string][]models.Event)
	for _, e := range events {
		key := e.StartsAt.Format("2006-01-02")
		out[key] = append(out[key], e)
	}
	return out
}

// forecastDailyByDayKey builds a "2006-01-02" -> weather.DayPoint lookup
// from the scheduler's already-cached forecast (see internal/weather's
// Cache/runWeatherRefresh) - never a fresh Open-Meteo call, same as every
// other kiosk weather read. Returns an empty map (not an error) if no
// forecast has been fetched yet or no location is configured, so a day's
// heading simply omits the forecast icon/temp rather than failing the whole
// view.
func forecastDailyByDayKey(cache *weather.Cache) map[string]weather.DayPoint {
	out := make(map[string]weather.DayPoint)
	forecast, ok := cache.Get()
	if !ok {
		return out
	}
	for _, dp := range forecast.Daily {
		out[dp.Date.Format("2006-01-02")] = dp
	}
	return out
}

// minEventHeightPct is the smallest HeightPct a positioned event block is
// ever given, so a very short (or zero-duration) event stays visible/tappable
// on the grid rather than collapsing to nothing.
const minEventHeightPct = 2.0

// positionEventsOnGrid computes each event's vertical Top/Height percentage
// within the configured grid range. All-day events are left unpositioned
// (Top/HeightPct zero, Clipped false) - the template renders them as a small
// chip above the grid instead, the same {{if .AllDay}} branching pattern
// _agenda.html/_calendar.html already use for flat list rendering.
//
// In "fixed" mode, an event that starts before grid.StartHour or ends after
// grid.EndHour is clipped/compressed to the grid edge (Clipped=true) rather
// than hidden or expanding the grid, so it stays visible without disturbing
// the configured range. In "full24" mode the range is always the whole day,
// so nothing is ever clipped.
func positionEventsOnGrid(events []models.Event, grid weekGridConfig) []weekEvent {
	rangeStart, rangeEnd := gridRangeMinutes(grid)
	span := float64(rangeEnd - rangeStart)

	out := make([]weekEvent, 0, len(events))
	for _, e := range events {
		we := weekEvent{Event: e}
		if e.AllDay {
			out = append(out, we)
			continue
		}

		startMin := minutesOfDay(e.StartsAt)
		endMin := minutesOfDay(e.EndsAt)
		if endMin <= startMin {
			// Zero/negative-duration event (EndsAt missing or equal to
			// StartsAt) - give it a minimum visible block anchored at its
			// start rather than disappearing.
			endMin = startMin + 30
		}

		clippedStart := clampInt(startMin, rangeStart, rangeEnd)
		clippedEnd := clampInt(endMin, rangeStart, rangeEnd)
		we.Clipped = grid.Mode == kioskGridModeFixed && (startMin < rangeStart || endMin > rangeEnd)

		we.TopPct = float64(clippedStart-rangeStart) / span * 100
		we.HeightPct = float64(clippedEnd-clippedStart) / span * 100
		if we.HeightPct < minEventHeightPct {
			we.HeightPct = minEventHeightPct
		}
		out = append(out, we)
	}
	return out
}

func gridRangeMinutes(grid weekGridConfig) (start, end int) {
	if grid.Mode == kioskGridModeFull24 {
		return 0, 24 * 60
	}
	return grid.StartHour * 60, grid.EndHour * 60
}

func minutesOfDay(t time.Time) int {
	return t.Hour()*60 + t.Minute()
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// hourLabelsForGrid returns one label per hour in the grid's range (e.g.
// "6 AM".."10 PM" for a fixed 6-22 range, "12 AM".."11 PM" for full24), each
// with a TopPct computed the same way positionEventsOnGrid computes an
// event's TopPct - rendered once as a shared hour-axis column rather than
// duplicated per day.
func hourLabelsForGrid(grid weekGridConfig) []weekHourLabel {
	start, end := gridRangeMinutes(grid)
	startHour, endHour := start/60, end/60
	span := float64(end - start)
	count := endHour - startHour

	labels := make([]weekHourLabel, 0, count)
	for i, h := 0, startHour; h < endHour; i, h = i+1, h+1 {
		t := time.Date(2000, 1, 1, h, 0, 0, 0, time.UTC)
		labels = append(labels, weekHourLabel{
			Label:  t.Format("3 PM"),
			TopPct: float64(i*60) / span * 100,
		})
	}
	return labels
}

// renderWeekDayChoresFragment re-fetches and re-renders just one day's chore
// list, backing the 5-day view's tap-to-complete flow (see
// KioskCompleteChore's ?view=week&day=... branch in kiosk.go). Re-fetching
// only the tapped day, rather than the full 5-day kioskWeekViewData, keeps a
// single chore tap cheap regardless of how many other days/events are on
// screen.
func (a *App) renderWeekDayChoresFragment(w http.ResponseWriter, r *http.Request, dateKey string) {
	ctx := r.Context()
	date, err := time.ParseInLocation("2006-01-02", dateKey, time.Local)
	if err != nil {
		http.Error(w, "invalid day", http.StatusBadRequest)
		return
	}

	chores, err := a.ChoreInstances.ListForDate(ctx, date)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	a.renderFragment(w, "kiosk/_week_day_chores", weekDay{
		DateKey: dateKey,
		Chores:  chores,
	})
}
