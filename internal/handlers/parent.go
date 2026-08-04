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
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/caldav"
	"github.com/mscreations/hhq/internal/email"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/release"
	"github.com/mscreations/hhq/internal/scheduler"
	"github.com/mscreations/hhq/internal/weather"
)

// errBootstrapManaged is a sentinel returned by checkChoreDefinitionNotBootstrapManaged
// to signal "already wrote a 403 response, stop here" to its caller.
var errBootstrapManaged = errors.New("bootstrap managed")

// accountWithCalendars pairs a calendar_account with the individual
// calendars discovered under it, for the dashboard's grouped display.
type accountWithCalendars struct {
	Account   models.CalendarAccount
	Calendars []models.Calendar
	Syncing   bool
}

type parentDashboardData struct {
	CurrentUser         *models.User
	AppTitle            string
	Timezone            string
	Timezones           []string
	SettingsError       string
	WeatherLocationName string
	WeatherUnits        string
	WeatherRadarZoom    int
	// KioskLayout/KioskGridMode/KioskGridStartHour/KioskGridEndHour back the
	// 5-day calendar view's settings - see kiosk_week.go.
	KioskLayout        string
	KioskGridMode      string
	KioskGridStartHour int
	KioskGridEndHour   int
	// WeeklyReportWeekday/Hour are the effective (settings-override-or-
	// config-default) schedule for the automatic weekly report email - see
	// scheduler.loadWeeklyReportSchedule. Weekdays holds the Sunday..Saturday
	// labels for the dashboard's weekday <select>, in time.Weekday order.
	WeeklyReportWeekday int
	WeeklyReportHour    int
	Weekdays            []string
	// Hours is 0..23, for the Weekly Report Time <select> - templates can't
	// easily generate an integer range on their own.
	Hours                 []int
	Children              []models.User
	Parents               []models.User
	AccountsWithCalendars []accountWithCalendars
	PaletteColors         []string
	AnyAccountSyncing     bool
	GoogleOAuthEnabled    bool
	GoogleError           string
	Chores                []models.Chore
	ChoreDefsByChild      []childChoreDefGroup
	// ChoreDefsByParent mirrors ChoreDefsByChild but groups the chores
	// assigned to each parent instead - informational only, see
	// models.ChoreDefinition.AssigneeRole.
	ChoreDefsByParent []childChoreDefGroup
	PendingApprovals  []models.ChoreInstance
	// ChildAvatarByID looks up a child/parent's avatar URL by user ID, for
	// the Pending Approvals card - avoids adding avatar columns to
	// ChoreInstance's own JOIN queries (see avatarURLsByUserID in kiosk.go).
	ChildAvatarByID map[int]string
	Plugins         []PluginRow
	// PluginSettingsError is set when PluginSettingsPage couldn't reach a
	// plugin - read back from the redirect query param the same way
	// InviteError/SettingsError are, so the dashboard can show it in a modal
	// on the next GET instead of navigating to a bare error page.
	PluginSettingsError string
	CSRFToken           string
	InviteError         string
	// ChildrenError is set when an avatar upload was rejected (bad file type,
	// too large, etc.) - read back from the redirect query param the same way
	// InviteError/SettingsError are.
	ChildrenError string
	// DisplayNameSavedID is the ID of the parent whose display name was just
	// saved (see SetParentDisplayName), so the Parents card can show a brief
	// confirmation next to that specific row. 0 means "nothing just saved".
	DisplayNameSavedID int
	// Version/UpdateAvailable/LatestReleaseURL feed the dashboard footer -
	// see buildParentDashboardData.
	Version          string
	UpdateAvailable  bool
	LatestReleaseURL string
}

// childChoreDefGroup pairs a child with their chore definitions, for the
// dashboard's per-child collapsible chore panels. Every child appears even
// if they have no chores defined yet.
type childChoreDefGroup struct {
	ChildID   int
	ChildName string
	Defs      []models.ChoreDefinition
}

// groupChoreDefsByUser groups chore definitions by assignee (child or
// parent) for the dashboard's per-assignee collapsible chore panels. Every
// user in the given list appears even if they have no chores defined yet.
func groupChoreDefsByUser(users []models.User, defs []models.ChoreDefinition) []childChoreDefGroup {
	byUser := map[int][]models.ChoreDefinition{}
	for _, d := range defs {
		byUser[d.ChildID] = append(byUser[d.ChildID], d)
	}
	groups := make([]childChoreDefGroup, 0, len(users))
	for _, u := range users {
		groups = append(groups, childChoreDefGroup{ChildID: u.ID, ChildName: u.Name, Defs: byUser[u.ID]})
	}
	return groups
}

// calendarAccountEditData wraps the account being edited with the current
// session's CSRF token, for the two forms on that page.
type calendarAccountEditData struct {
	Account   *models.CalendarAccount
	CSRFToken string
	AppTitle  string
}

// ParentDashboard is the main settings/management screen. Protected by
// SessionMgr.RequireParent middleware (see cmd/server/main.go routes).
func (a *App) ParentDashboard(w http.ResponseWriter, r *http.Request) {
	data, err := a.buildParentDashboardData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderFragment(w, "parent/dashboard", *data)
}

func (a *App) buildParentDashboardData(r *http.Request) (*parentDashboardData, error) {
	ctx := r.Context()

	appTitle, err := a.Settings.Get(ctx, "app_title", a.Cfg.AppTitle)
	if err != nil {
		return nil, err
	}
	timezone, err := a.Settings.Get(ctx, "timezone", "America/New_York")
	if err != nil {
		return nil, err
	}
	weatherLocationName, err := a.Settings.Get(ctx, weather.SettingLocationName, "")
	if err != nil {
		return nil, err
	}
	weatherUnits, err := a.Settings.Get(ctx, weather.SettingUnits, string(weather.UnitsImperial))
	if err != nil {
		return nil, err
	}
	weatherRadarZoom, err := weather.LoadRadarZoom(ctx, a.Settings)
	if err != nil {
		return nil, err
	}
	weeklyReportWeekday, weeklyReportHour, err := a.loadWeeklyReportScheduleSetting(ctx)
	if err != nil {
		return nil, err
	}
	kioskLayout, err := a.Settings.Get(ctx, settingKioskLayout, kioskLayoutClassic)
	if err != nil {
		return nil, err
	}
	kioskGrid, err := a.loadWeekGridConfig(ctx)
	if err != nil {
		return nil, err
	}

	children, err := a.Users.ListChildren(ctx)
	if err != nil {
		return nil, err
	}
	parents, err := a.Users.ListParents(ctx)
	if err != nil {
		return nil, err
	}

	accounts, err := a.CalendarAccounts.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	// One query per account to fetch its calendars - the accounts list is
	// small (a handful of family email logins at most), so the simplicity
	// of this N+1 pattern is worth it over a more complex single grouped
	// query, especially given this project's goal of staying readable.
	accountsWithCalendars := make([]accountWithCalendars, 0, len(accounts))
	for _, acct := range accounts {
		cals, err := a.Calendars.ListForAccount(ctx, acct.ID)
		if err != nil {
			return nil, err
		}
		accountsWithCalendars = append(accountsWithCalendars, accountWithCalendars{
			Account:   acct,
			Calendars: cals,
			Syncing:   a.syncing.isSyncing(acct.ID),
		})
	}

	choreCatalog, err := a.Chores.ListActive(ctx)
	if err != nil {
		return nil, err
	}

	choreDefs, err := a.ChoreDefs.ListActive(ctx)
	if err != nil {
		return nil, err
	}

	pluginList, err := a.Plugins.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	pluginRows := a.buildPluginRows(pluginList)

	// Pending approvals: pull this week's instances and filter client-side here
	// since it's a small dataset; fine to optimize with a dedicated query later.
	weekStart := startOfWeek(time.Now())
	weekInstances, err := a.ChoreInstances.ListForWeek(ctx, weekStart)
	if err != nil {
		return nil, err
	}
	var pending []models.ChoreInstance
	for _, ci := range weekInstances {
		if ci.Status == models.StatusPendingApproval {
			pending = append(pending, ci)
		}
	}

	avatarByID, err := a.avatarURLsByUserID(ctx)
	if err != nil {
		return nil, err
	}

	updateAvailable, latestReleaseURL := a.checkUpdateAvailable()

	return &parentDashboardData{
		CurrentUser:           auth.UserFromContext(ctx),
		AppTitle:              appTitle,
		Timezone:              timezone,
		Timezones:             models.CommonTimezones,
		Children:              children,
		Parents:               parents,
		AccountsWithCalendars: accountsWithCalendars,
		PaletteColors:         models.PaletteColors(),
		AnyAccountSyncing:     a.syncing.any(),
		GoogleOAuthEnabled:    a.Cfg.GoogleOAuthEnabled(),
		GoogleError:           r.URL.Query().Get("google_error"),
		Chores:                choreCatalog,
		ChoreDefsByChild:      groupChoreDefsByUser(children, choreDefs),
		ChoreDefsByParent:     groupChoreDefsByUser(parents, choreDefs),
		PendingApprovals:      pending,
		ChildAvatarByID:       avatarByID,
		Plugins:               pluginRows,
		PluginSettingsError:   r.URL.Query().Get("plugin_settings_error"),
		CSRFToken:             a.SessionMgr.CSRFToken(a.CSRF, r),
		InviteError:           r.URL.Query().Get("invite_error"),
		ChildrenError:         r.URL.Query().Get("children_error"),
		SettingsError:         r.URL.Query().Get("settings_error"),
		WeatherLocationName:   weatherLocationName,
		WeatherUnits:          weatherUnits,
		WeatherRadarZoom:      weatherRadarZoom,
		KioskLayout:           kioskLayout,
		KioskGridMode:         kioskGrid.Mode,
		KioskGridStartHour:    kioskGrid.StartHour,
		KioskGridEndHour:      kioskGrid.EndHour,
		DisplayNameSavedID:    parseIntOrZero(r.URL.Query().Get("display_name_saved")),
		WeeklyReportWeekday:   weeklyReportWeekday,
		WeeklyReportHour:      weeklyReportHour,
		Weekdays:              []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
		Hours:                 hoursOfDay,
		Version:               a.Version,
		UpdateAvailable:       updateAvailable,
		LatestReleaseURL:      latestReleaseURL,
	}, nil
}

// PluginRow wraps a models.Plugin with the dashboard's per-plugin
// update-available check (see buildPluginRows) - kept separate from
// models.Plugin itself since UpdateAvailable/LatestVersion/Changelog are
// derived per-request from a.PluginVersions (each plugin's own self-reported
// GET /version - see internal/plugins/version.go), not stored on the plugin.
type PluginRow struct {
	models.Plugin
	UpdateAvailable bool
	LatestVersion   string
	Changelog       string
	// PreRelease is true when this plugin self-reports a "dev" channel (see
	// internal/plugins/version.go's VersionInfo.Channel) while hhq itself is
	// running a non-dev (production) build - flags a pre-release plugin that
	// may have issues, running alongside a stable hhq.
	PreRelease bool
}

// buildPluginRows pairs each plugin with the scheduler's cached GET /version
// result (see scheduler.checkPluginVersions). A plugin with nothing cached
// yet (e.g. app just started, or the plugin hasn't responded) simply gets
// UpdateAvailable = false and PreRelease = false.
func (a *App) buildPluginRows(pluginList []models.Plugin) []PluginRow {
	hhqIsDev := strings.HasSuffix(a.Version, "-dev")
	rows := make([]PluginRow, len(pluginList))
	for i, p := range pluginList {
		rows[i] = PluginRow{Plugin: p}
		if a.PluginVersions == nil {
			continue
		}
		info, ok := a.PluginVersions.Get(p.ID)
		if !ok {
			continue
		}
		rows[i].PreRelease = !hhqIsDev && info.Channel == "dev"
		if !info.UpgradeAvailable {
			continue
		}
		rows[i].UpdateAvailable = true
		rows[i].LatestVersion = info.UpgradeVersion
		rows[i].Changelog = info.Changelog
	}
	return rows
}

// checkUpdateAvailable compares the running version against the latest
// release the scheduler's background job has cached (see
// scheduler.runReleaseCheck) and reports whether a newer version is
// available, plus its release-notes URL. Returns (false, "") if nothing has
// been fetched yet (e.g. app just started, or GitHub was unreachable).
func (a *App) checkUpdateAvailable() (bool, string) {
	if a.Release == nil {
		return false, ""
	}
	latest, ok := a.Release.Get()
	if !ok {
		return false, ""
	}
	if !release.IsNewer(a.Version, latest.Version) {
		return false, ""
	}
	return true, latest.URL
}

// hoursOfDay is 0..23, computed once at package init rather than per-request
// since it never changes - used by the Weekly Report Time <select>.
var hoursOfDay = func() []int {
	hours := make([]int, 24)
	for i := range hours {
		hours[i] = i
	}
	return hours
}()

// loadWeeklyReportScheduleSetting reads the dashboard-configurable weekly
// report weekday/hour override out of settings, falling back to the
// env-var-sourced config defaults when unset or invalid - mirrors
// scheduler.loadWeeklyReportSchedule (kept as a separate small read here
// since that method lives on *Scheduler and this package has no Scheduler
// instance to call it on).
func (a *App) loadWeeklyReportScheduleSetting(ctx context.Context) (weekday int, hour int, err error) {
	weekday = int(a.Cfg.WeeklyReportWeekday)
	hour = a.Cfg.WeeklyReportHour

	weekdayStr, err := a.Settings.Get(ctx, scheduler.SettingWeeklyReportWeekday, "")
	if err != nil {
		return 0, 0, err
	}
	if weekdayStr != "" {
		if n, convErr := strconv.Atoi(weekdayStr); convErr == nil && n >= 0 && n <= 6 {
			weekday = n
		}
	}

	hourStr, err := a.Settings.Get(ctx, scheduler.SettingWeeklyReportHour, "")
	if err != nil {
		return 0, 0, err
	}
	if hourStr != "" {
		if n, convErr := strconv.Atoi(hourStr); convErr == nil && n >= 0 && n <= 23 {
			hour = n
		}
	}

	return weekday, hour, nil
}

// parseIntOrZero parses s as an int, returning 0 for a blank or invalid
// string rather than an error - used for optional query-param flags like
// "display_name_saved" where "not present" and "zero" mean the same thing.
func parseIntOrZero(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// respondAfterMutation is the shared tail end of every dashboard mutation
// handler. Plain form posts (no JS, or htmx unavailable) get the classic
// full-page redirect back to /parent. Requests from htmx (identified by the
// HX-Request header htmx sets on every request it issues) instead get back
// just the named fragment re-rendered with fresh data, which htmx swaps into
// the matching hx-target - this is what gives the dashboard its "smoother"
// feel without a full page reload per action, while leaving the non-JS
// fallback behavior unchanged.
// oobFragments are additional fragments appended to the response using
// htmx's hx-swap-oob mechanism (see web/templates/parent/_chore_defs.html's
// "_chore_defs_oob" variant) - used when a mutation on one card needs to
// refresh data displayed on another card too (e.g. adding/removing a child
// changes the child <select> on the Chores card, which htmx wouldn't
// otherwise know to refresh since it's not the primary hx-target).
func (a *App) respondAfterMutation(w http.ResponseWriter, r *http.Request, fragment string, oobFragments ...string) {
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/parent", http.StatusSeeOther)
		return
	}
	data, err := a.buildParentDashboardData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderFragment(w, fragment, *data)
	for _, oob := range oobFragments {
		a.renderFragment(w, oob, *data)
	}
}

// respondParentsError is the error-path counterpart to respondAfterMutation
// for the two failure cases on the "Parents" card (duplicate invite email,
// removing the last parent). A plain form post redirects with the message in
// a query param (read back by buildParentDashboardData on the next GET); an
// htmx request instead gets the "_parents" fragment re-rendered immediately
// with the error, no redirect round-trip needed.
func (a *App) respondParentsError(w http.ResponseWriter, r *http.Request, message string) {
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/parent?invite_error="+url.QueryEscape(message), http.StatusSeeOther)
		return
	}
	data, err := a.buildParentDashboardData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data.InviteError = message
	a.renderFragment(w, "parent/_parents", *data)
}

// respondSettingsError is the error-path counterpart to respondAfterMutation
// for the "Settings" card (currently just an invalid timezone). Mirrors
// respondParentsError's pattern.
func (a *App) respondSettingsError(w http.ResponseWriter, r *http.Request, message string) {
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/parent?settings_error="+url.QueryEscape(message), http.StatusSeeOther)
		return
	}
	data, err := a.buildParentDashboardData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data.SettingsError = message
	a.renderFragment(w, "parent/_settings", *data)
}

// --- Settings ---

// UpdateSettings handles the dashboard's "Settings" card - currently the app
// title (shown on the kiosk and dashboard headers) and timezone.
func (a *App) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	title := r.FormValue("app_title")
	if title == "" {
		title = "HappyHome Quest"
	}
	timezone := r.FormValue("timezone")
	if timezone == "" {
		timezone = "America/New_York"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		logging.Warnf("parent: rejecting invalid timezone %q: %v", timezone, err)
		a.respondSettingsError(w, r, "Not a recognized timezone: "+timezone)
		return
	}

	if err := a.Settings.Set(r.Context(), "app_title", title); err != nil {
		logging.Errorf("parent: updating app_title setting: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.Settings.Set(r.Context(), "timezone", timezone); err != nil {
		logging.Errorf("parent: updating timezone setting: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Both fields are blank-means-keep-existing (same convention as
	// location_query/radar_zoom below) so that posting the form without
	// these two <select>s present (or with them left at their rendered
	// default) never clobbers an already-configured schedule.
	if weeklyReportWeekdayStr := r.FormValue("weekly_report_weekday"); weeklyReportWeekdayStr != "" {
		weeklyReportWeekday, err := strconv.Atoi(weeklyReportWeekdayStr)
		if err != nil || weeklyReportWeekday < 0 || weeklyReportWeekday > 6 {
			logging.Warnf("parent: rejecting invalid weekly report weekday %q", weeklyReportWeekdayStr)
			a.respondSettingsError(w, r, "Weekly report day must be a day of the week.")
			return
		}
		if err := a.Settings.Set(r.Context(), scheduler.SettingWeeklyReportWeekday, strconv.Itoa(weeklyReportWeekday)); err != nil {
			logging.Errorf("parent: updating weekly report weekday setting: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if weeklyReportHourStr := r.FormValue("weekly_report_hour"); weeklyReportHourStr != "" {
		weeklyReportHour, err := strconv.Atoi(weeklyReportHourStr)
		if err != nil || weeklyReportHour < 0 || weeklyReportHour > 23 {
			logging.Warnf("parent: rejecting invalid weekly report hour %q", weeklyReportHourStr)
			a.respondSettingsError(w, r, "Weekly report time must be an hour between 0 and 23.")
			return
		}
		if err := a.Settings.Set(r.Context(), scheduler.SettingWeeklyReportHour, strconv.Itoa(weeklyReportHour)); err != nil {
			logging.Errorf("parent: updating weekly report hour setting: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// The 4 kiosk fields below are all blank-means-keep-existing (same
	// convention as weekly_report_weekday/hour above), so posting this form
	// without them - e.g. a request that predates this feature, or any
	// partial submission - never clobbers an already-configured layout.
	if kioskLayoutStr := r.FormValue("kiosk_layout"); kioskLayoutStr != "" {
		if kioskLayoutStr != kioskLayoutWeekly {
			kioskLayoutStr = kioskLayoutClassic
		}
		if err := a.Settings.Set(r.Context(), settingKioskLayout, kioskLayoutStr); err != nil {
			logging.Errorf("parent: updating kiosk_layout setting: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if kioskGridModeStr := r.FormValue("kiosk_grid_mode"); kioskGridModeStr != "" {
		if kioskGridModeStr != kioskGridModeFull24 {
			kioskGridModeStr = kioskGridModeFixed
		}
		if err := a.Settings.Set(r.Context(), settingKioskGridMode, kioskGridModeStr); err != nil {
			logging.Errorf("parent: updating kiosk_grid_mode setting: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	kioskGridStartHourStr := r.FormValue("kiosk_grid_start_hour")
	kioskGridEndHourStr := r.FormValue("kiosk_grid_end_hour")
	if kioskGridStartHourStr != "" || kioskGridEndHourStr != "" {
		current, err := a.loadWeekGridConfig(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		kioskGridStartHour, kioskGridEndHour := current.StartHour, current.EndHour
		if kioskGridStartHourStr != "" {
			h, ok := parseHour(kioskGridStartHourStr)
			if !ok {
				logging.Warnf("parent: rejecting invalid kiosk grid start hour %q", kioskGridStartHourStr)
				a.respondSettingsError(w, r, "Calendar view start hour must be between 0 and 23.")
				return
			}
			kioskGridStartHour = h
		}
		if kioskGridEndHourStr != "" {
			h, ok := parseHour(kioskGridEndHourStr)
			if !ok {
				logging.Warnf("parent: rejecting invalid kiosk grid end hour %q", kioskGridEndHourStr)
				a.respondSettingsError(w, r, "Calendar view end hour must be between 0 and 23.")
				return
			}
			kioskGridEndHour = h
		}
		if kioskGridEndHour <= kioskGridStartHour {
			a.respondSettingsError(w, r, "Calendar view end hour must be after the start hour.")
			return
		}
		if err := a.Settings.Set(r.Context(), settingKioskGridStartHour, strconv.Itoa(kioskGridStartHour)); err != nil {
			logging.Errorf("parent: updating kiosk_grid_start_hour setting: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := a.Settings.Set(r.Context(), settingKioskGridEndHour, strconv.Itoa(kioskGridEndHour)); err != nil {
			logging.Errorf("parent: updating kiosk_grid_end_hour setting: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	units := r.FormValue("units")
	if units != string(weather.UnitsMetric) {
		units = string(weather.UnitsImperial)
	}
	if err := a.Settings.Set(r.Context(), weather.SettingUnits, units); err != nil {
		logging.Errorf("parent: updating weather units setting: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// A blank radar_zoom means "keep the existing value" (same convention as
	// location_query below), since this field is easy to leave untouched
	// when only changing something else on this form.
	if radarZoomStr := r.FormValue("radar_zoom"); radarZoomStr != "" {
		radarZoom, err := strconv.Atoi(radarZoomStr)
		if err != nil || radarZoom < 1 || radarZoom > 18 {
			logging.Warnf("parent: rejecting invalid radar zoom %q", radarZoomStr)
			a.respondSettingsError(w, r, "Radar zoom must be a number between 1 and 18.")
			return
		}
		if err := a.Settings.Set(r.Context(), weather.SettingRadarZoom, strconv.Itoa(radarZoom)); err != nil {
			logging.Errorf("parent: updating weather radar zoom setting: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// A blank location_query means "keep the existing location" (same
	// blank-means-keep-existing convention as the calendar account password
	// field) - only re-geocode when the parent actually typed something.
	locationQuery := r.FormValue("location_query")
	if locationQuery != "" {
		loc, err := weather.Geocode(r.Context(), locationQuery)
		if err != nil {
			logging.Warnf("parent: geocoding location %q failed: %v", locationQuery, err)
			a.respondSettingsError(w, r, "Couldn't find that location: "+err.Error())
			return
		}
		if err := a.Settings.Set(r.Context(), weather.SettingLocationName, loc.Name); err != nil {
			logging.Errorf("parent: updating weather location name setting: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := a.Settings.Set(r.Context(), weather.SettingLat, strconv.FormatFloat(loc.Lat, 'f', -1, 64)); err != nil {
			logging.Errorf("parent: updating weather lat setting: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := a.Settings.Set(r.Context(), weather.SettingLon, strconv.FormatFloat(loc.Lon, 'f', -1, 64)); err != nil {
			logging.Errorf("parent: updating weather lon setting: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		logging.Infof("parent: updated weather location to %q", loc.Name)
		a.refreshWeatherAsync()
	}

	logging.Infof("parent: updated settings (app_title=%q, timezone=%q)", title, timezone)
	a.respondAfterMutation(w, r, "parent/_settings")
}

// --- Users ---

func (a *App) CreateChild(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	color, err := a.Users.NextAvailableColor(r.Context())
	if err != nil {
		logging.Errorf("parent: picking color for child %q: %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := a.Users.CreateChild(r.Context(), name, color); err != nil {
		logging.Errorf("parent: creating child %q: %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: created child %q", name)
	a.respondAfterMutation(w, r, "parent/_children", "parent/_chore_defs_oob")
}

// CreateParent handles the dashboard's "Send Invite" form: it no longer takes
// a password (the admin shouldn't know other parents' passwords) - it creates
// a pending parent row with no password_hash and emails the invitee a signed
// link to set their own password (see InviteAcceptPage/InviteAcceptSubmit).
func (a *App) CreateParent(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	recipientEmail := r.FormValue("email")

	id, err := a.Users.InviteParent(r.Context(), name, recipientEmail)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			logging.Warnf("parent: invite to %q failed, email already in use", recipientEmail)
			a.respondParentsError(w, r, "That email is already registered.")
			return
		}
		logging.Errorf("parent: inviting parent %q: %v", recipientEmail, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	inviter := auth.UserFromContext(r.Context())
	inviterName := "A parent"
	if inviter != nil {
		inviterName = inviter.Name
	}
	if err := a.sendInviteEmail(r.Context(), id, name, recipientEmail, inviterName); err != nil {
		logging.Errorf("parent: sending invite email to %q: %v", recipientEmail, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: invited new parent %q (id=%d)", recipientEmail, id)
	a.respondAfterMutation(w, r, "parent/_parents")
}

// ResendParentInvite re-sends the invite email for a parent who hasn't yet
// accepted (password_hash still NULL) - e.g. if the original link expired.
func (a *App) ResendParentInvite(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	user, err := a.Users.GetByID(r.Context(), id)
	if err == models.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if user.Role != models.RoleParent || user.PasswordHash.Valid {
		http.Error(w, "invite already accepted or not a pending parent", http.StatusBadRequest)
		return
	}

	if err := a.Users.MarkInvited(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	inviter := auth.UserFromContext(r.Context())
	inviterName := "A parent"
	if inviter != nil {
		inviterName = inviter.Name
	}
	if err := a.sendInviteEmail(r.Context(), id, user.Name, user.Email.String, inviterName); err != nil {
		logging.Errorf("parent: resending invite email to %q: %v", user.Email.String, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: resent invite to %q (id=%d)", user.Email.String, id)
	a.respondAfterMutation(w, r, "parent/_parents")
}

// SetParentDisplayName sets or clears a parent's kiosk display name (e.g.
// "Mom"/"Dad", shown on the chore tracker instead of their real name - see
// models.User.DisplayLabel). A blank submitted value clears it.
func (a *App) SetParentDisplayName(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	user, err := a.Users.GetByID(r.Context(), id)
	if err == models.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if user.BootstrapManaged {
		http.Error(w, "this parent is managed by parents.json config and can't be edited here", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := a.Users.SetDisplayName(r.Context(), id, r.FormValue("display_name")); err != nil {
		logging.Errorf("parent: setting display name for user id=%d: %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: set display name for user id=%d", id)
	a.respondDisplayNameSaved(w, r, id)
}

// respondDisplayNameSaved is respondAfterMutation's counterpart for
// SetParentDisplayName specifically, so the Parents card can show a "Saved"
// confirmation next to the row that was just updated (see
// parentDashboardData.DisplayNameSavedID) - respondAfterMutation has no way
// to thread that extra piece of per-request state through.
func (a *App) respondDisplayNameSaved(w http.ResponseWriter, r *http.Request, id int) {
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, fmt.Sprintf("/parent?display_name_saved=%d", id), http.StatusSeeOther)
		return
	}
	data, err := a.buildParentDashboardData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data.DisplayNameSavedID = id
	a.renderFragment(w, "parent/_parents", *data)
}

// RemoveUser deactivates a parent or child (soft delete via is_active=FALSE,
// same mechanism as models.UserStore.Deactivate) - the row and its history
// (chore instances, approvals, etc.) are kept, but the user drops out of
// ListChildren/ListParents/login. Removing the last remaining parent is
// blocked, since that would lock everyone out of /parent (with no invite
// flow left to recover - only an existing parent can invite another).
func (a *App) RemoveUser(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	user, err := a.Users.GetByID(r.Context(), id)
	if err == models.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if user.Role == models.RoleParent {
		parents, err := a.Users.ListParents(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(parents) <= 1 {
			logging.Warnf("parent: refusing to remove last remaining parent %q (id=%d)", user.Email.String, id)
			a.respondParentsError(w, r, "Can't remove the last remaining parent.")
			return
		}
	}
	if user.BootstrapManaged {
		if user.Role == models.RoleParent {
			http.Error(w, "this parent is managed by parents.json config and can't be removed here", http.StatusForbidden)
		} else {
			http.Error(w, "this child is managed by children.json config and can't be removed here", http.StatusForbidden)
		}
		return
	}

	if err := a.Users.Deactivate(r.Context(), id); err != nil {
		logging.Errorf("parent: removing user %q (id=%d): %v", user.Name, id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: removed %s %q (id=%d)", user.Role, user.Name, id)
	if user.Role == models.RoleParent {
		a.respondAfterMutation(w, r, "parent/_parents")
	} else {
		a.respondAfterMutation(w, r, "parent/_children", "parent/_chore_defs_oob")
	}
}

func (a *App) sendInviteEmail(ctx context.Context, userID int, recipientName, recipientEmail, inviterName string) error {
	token := a.Invite.Sign(userID, auth.ActionInviteAccept, a.Cfg.InviteLinkTTL)
	acceptURL := a.Cfg.PublicBaseURL + "/invite/accept?token=" + token

	appTitle, err := a.Settings.Get(ctx, "app_title", a.Cfg.AppTitle)
	if err != nil {
		return err
	}
	subject, htmlBody, err := email.RenderInviteEmail(email.InviteEmailData{
		AppName:       appTitle,
		RecipientName: recipientName,
		InviterName:   inviterName,
		AcceptURL:     acceptURL,
	})
	if err != nil {
		return err
	}
	return a.Mailer.Send(appTitle, []string{recipientEmail}, subject, htmlBody)
}

// --- Calendar accounts ---

func (a *App) CreateCalendarAccount(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	provider := models.CalendarProvider(r.FormValue("provider"))
	name := r.FormValue("name")
	caldavURL := r.FormValue("caldav_url")
	username := r.FormValue("username")
	password := r.FormValue("password")

	// Pre-fill the standard CalDAV root for the two required providers so the
	// parent doesn't need to know the exact URL. They can still override it.
	caldavURL = models.DefaultCalDAVURL(provider, caldavURL)

	encrypted, err := a.Encryptor.Encrypt(password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	id, err := a.CalendarAccounts.Create(r.Context(), models.CalendarAccount{
		Name:              name,
		Provider:          provider,
		CalDAVURL:         nullString(caldavURL),
		Username:          nullString(username),
		EncryptedPassword: encrypted,
	})
	if err != nil {
		logging.Errorf("parent: creating calendar account %q: %v", name, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: created calendar account %q (id=%d, provider=%s, url=%s)", name, id, provider, caldavURL)

	// Discover the account's calendars synchronously (fast - just a listing,
	// no event fetching) so the parent sees the calendar list with
	// auto-assigned colors immediately after being redirected back to the
	// dashboard, rather than an empty list until the next background sync.
	discoverCtx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if account, err := a.CalendarAccounts.GetByID(discoverCtx, id); err != nil {
		logging.Errorf("parent: reloading newly created account id=%d: %v", id, err)
	} else if discovered, derr := caldav.DiscoverAndUpsertCalendars(discoverCtx, a.Calendars, *account, password); derr != nil {
		logging.Warnf("parent: initial calendar discovery for %q failed (will retry on next scheduled sync): %v", name, derr)
		_ = a.CalendarAccounts.MarkSynced(discoverCtx, id, derr)
	} else {
		logging.Infof("parent: discovered %d calendar(s) for %q", len(discovered), name)
	}

	// Fetch actual events for the newly-discovered (enabled-by-default)
	// calendars in the background, so the redirect above doesn't also wait
	// on that - discovery above already made the calendar list itself
	// visible; this just fills in their events shortly after.
	a.syncAccountAsync(id)

	a.respondAfterMutation(w, r, "parent/_calendar_accounts")
}

// EditCalendarAccountPage shows a pre-filled edit form. The password field is
// intentionally left blank - submitting the form without touching it leaves
// the currently stored (encrypted) password unchanged.
func (a *App) EditCalendarAccountPage(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	account, err := a.CalendarAccounts.GetByID(r.Context(), id)
	if err == models.ErrAccountNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if account.BootstrapManaged {
		http.Error(w, "this account is managed by CALENDAR_ACCOUNTS config and can't be edited here", http.StatusForbidden)
		return
	}
	a.renderFragment(w, "parent/calendar_account_edit", calendarAccountEditData{
		Account:   account,
		CSRFToken: a.SessionMgr.CSRFToken(a.CSRF, r),
		AppTitle:  a.appTitle(r.Context()),
	})
}

func (a *App) UpdateCalendarAccount(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	account, err := a.CalendarAccounts.GetByID(r.Context(), id)
	if err == models.ErrAccountNotFound {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if account.BootstrapManaged {
		http.Error(w, "this account is managed by CALENDAR_ACCOUNTS config and can't be edited here", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")

	if account.Provider == models.ProviderGoogle {
		// A Google account's edit form only ever offers a label - there's no
		// url/username/password to submit, so this never touches those
		// columns (see UpdateGoogleName).
		if err := a.CalendarAccounts.UpdateGoogleName(r.Context(), id, name); err != nil {
			logging.Errorf("parent: renaming google calendar account id=%d: %v", id, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		logging.Infof("parent: renamed google calendar account id=%d", id)
		http.Redirect(w, r, "/parent", http.StatusSeeOther)
		return
	}

	caldavURL := r.FormValue("caldav_url")
	username := r.FormValue("username")
	password := r.FormValue("password") // blank = keep existing

	var newEncrypted []byte
	if password != "" {
		newEncrypted, err = a.Encryptor.Encrypt(password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := a.CalendarAccounts.Update(r.Context(), id, name, caldavURL, username, newEncrypted); err != nil {
		logging.Errorf("parent: updating calendar account id=%d: %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: updated calendar account id=%d (password_changed=%v)", id, newEncrypted != nil)

	// Re-discover + re-sync immediately so a credential/URL fix takes effect
	// right away instead of waiting for the next scheduled tick.
	a.syncAccountAsync(id)

	http.Redirect(w, r, "/parent", http.StatusSeeOther)
}

func (a *App) DeleteCalendarAccount(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if account, err := a.CalendarAccounts.GetByID(r.Context(), id); err == models.ErrAccountNotFound {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if account.BootstrapManaged {
		http.Error(w, "this account is managed by CALENDAR_ACCOUNTS config and can't be deleted here", http.StatusForbidden)
		return
	}
	if err := a.CalendarAccounts.Delete(r.Context(), id); err != nil {
		logging.Errorf("parent: deleting calendar account id=%d: %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: deleted calendar account id=%d", id)
	http.Redirect(w, r, "/parent", http.StatusSeeOther)
}

// ResyncCalendarAccount lets a parent force an immediate discovery+sync pass
// for one account from the dashboard ("Resync Now" button), rather than
// waiting for the next scheduled tick (every CALENDAR_SYNC_INTERVAL_MINUTES).
func (a *App) ResyncCalendarAccount(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	logging.Infof("parent: manual resync requested for calendar account id=%d", id)
	a.syncAccountAsync(id)
	a.respondAfterMutation(w, r, "parent/_calendar_accounts")
}

// RefreshCalendarAccounts re-renders the calendar accounts card without
// performing any mutation. It's what the card's own self-polling
// (web/templates/parent/_calendar_accounts.html, gated on AnyAccountSyncing)
// calls to notice a background "Resync Now" sync finishing and pick up the
// resulting last-synced timestamp / cleared busy state.
func (a *App) RefreshCalendarAccounts(w http.ResponseWriter, r *http.Request) {
	data, err := a.buildParentDashboardData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderFragment(w, "parent/_calendar_accounts", *data)
}

// --- Individual calendars ---

// ToggleCalendarEnabled flips one calendar's enabled/disabled state. This is
// independent per-calendar - one Fastmail or iCloud login often has several
// calendars, and a parent may only want some of them (e.g. "Home" but not
// a shared "Work" calendar) shown on the kiosk.
func (a *App) ToggleCalendarEnabled(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	cal, err := a.Calendars.GetByID(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	newState := !cal.Enabled
	if err := a.Calendars.SetEnabled(r.Context(), id, newState); err != nil {
		logging.Errorf("parent: toggling calendar id=%d: %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: calendar id=%d (%q) set enabled=%v", id, cal.Name, newState)
	a.respondAfterMutation(w, r, "parent/_calendar_accounts")
}

// SetCalendarColor lets a parent override a calendar's auto-assigned color.
// The submitted value must be one of models.PaletteColors() - restricted to
// the known palette (rather than accepting an arbitrary hex value) so the
// dashboard's color picker stays a closed set of pre-vetted, visually
// distinct choices instead of free-form text input.
func (a *App) SetCalendarColor(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	color := r.FormValue("color")
	valid := false
	for _, c := range models.PaletteColors() {
		if c == color {
			valid = true
			break
		}
	}
	if !valid {
		http.Error(w, "invalid color", http.StatusBadRequest)
		return
	}
	if err := a.Calendars.SetColor(r.Context(), id, color); err != nil {
		logging.Errorf("parent: setting calendar id=%d color: %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: calendar id=%d set color=%s", id, color)
	a.respondAfterMutation(w, r, "parent/_calendar_accounts")
}

// --- Chores ---

// CreateChoreDefinition assigns a chore to a child (for points/approval
// tracking) or a parent (informational only - see models.ChoreDefinition.
// AssigneeRole) on a schedule. The "chore_id" field is either an existing
// catalog chore's ID, or the literal "new" - in which case
// "new_chore_name"/"description" are used to create a fresh catalog entry
// first, shared by any other child/parent it's later assigned to (see
// internal/models.ChoreStore).
func (a *App) CreateChoreDefinition(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	childID, err := strconv.Atoi(r.FormValue("child_id"))
	if err != nil {
		http.Error(w, "invalid child", http.StatusBadRequest)
		return
	}
	assignee, err := a.Users.GetByID(r.Context(), childID)
	if err != nil {
		http.Error(w, "invalid assignee", http.StatusBadRequest)
		return
	}
	points, _ := strconv.Atoi(r.FormValue("points"))
	if points <= 0 {
		points = 1
	}
	if assignee.Role == models.RoleParent {
		// Informational-only assignment: no points, regardless of what was
		// submitted (the points field is hidden/disabled client-side for a
		// parent assignee, but enforce it here too).
		points = 0
	}

	choreIDStr := r.FormValue("chore_id")
	createdNewChore := false
	var choreID int
	if choreIDStr == "" || choreIDStr == "new" {
		name := r.FormValue("new_chore_name")
		if name == "" {
			http.Error(w, "chore name required", http.StatusBadRequest)
			return
		}
		id, err := a.Chores.Create(r.Context(), name, r.FormValue("description"))
		if err != nil {
			logging.Errorf("parent: creating chore %q: %v", name, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		choreID = id
		createdNewChore = true
		logging.Infof("parent: created chore %q (id=%d)", name, choreID)
	} else {
		choreID, err = strconv.Atoi(choreIDStr)
		if err != nil {
			http.Error(w, "invalid chore", http.StatusBadRequest)
			return
		}
	}

	kind := r.FormValue("kind") // "recurring" or "one_off"
	switch kind {
	case "one_off":
		dateStr := r.FormValue("one_off_date")
		date, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			http.Error(w, "invalid date", http.StatusBadRequest)
			return
		}
		_, err = a.ChoreDefs.CreateOneOff(r.Context(), childID, choreID, points, date)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default: // recurring
		mask := 0
		for _, day := range r.Form["days"] { // e.g. checkboxes named "days" with values 0-6
			d, err := strconv.Atoi(day)
			if err != nil {
				continue
			}
			mask |= models.WeekdayBit(time.Weekday(d))
		}
		if mask == 0 {
			http.Error(w, "select at least one day", http.StatusBadRequest)
			return
		}
		_, err := a.ChoreDefs.CreateRecurring(r.Context(), childID, choreID, points, mask)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if createdNewChore {
		// The chore-select dropdown on this same card already reflects the
		// new chore (rebuilt from fresh data), but the separate catalog card
		// doesn't share a hx-target with this form, so it needs an
		// out-of-band refresh to show the new entry too.
		a.respondAfterMutation(w, r, "parent/_chore_defs", "parent/_chore_catalog_oob")
	} else {
		a.respondAfterMutation(w, r, "parent/_chore_defs")
	}
}

func (a *App) DeactivateChoreDefinition(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := a.checkChoreDefinitionNotBootstrapManaged(w, r, id); err != nil {
		return
	}
	if err := a.ChoreDefs.Deactivate(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.respondAfterMutation(w, r, "parent/_chore_defs")
}

// checkChoreDefinitionNotBootstrapManaged writes a 403 response and returns a
// non-nil error if the chore definition is bootstrap-managed (i.e. came from
// assignments.json), since editing/removing it here would just get silently
// clobbered on the next restart. Any error/response has already been written
// to w by the time this returns non-nil.
func (a *App) checkChoreDefinitionNotBootstrapManaged(w http.ResponseWriter, r *http.Request, id int) error {
	// ListActive is the only lookup available on ChoreDefinitionStore that
	// returns BootstrapManaged; the dataset is small (a handful of chores per
	// family), so scanning it here is simpler than adding a dedicated
	// GetByID just for this check.
	defs, err := a.ChoreDefs.ListActive(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return err
	}
	for _, d := range defs {
		if d.ID == id && d.BootstrapManaged {
			http.Error(w, "this assignment is managed by assignments.json config and can't be changed here", http.StatusForbidden)
			return errBootstrapManaged
		}
	}
	return nil
}

// UpdateChore renames/redescribes a catalog chore (the "Edit" button in the
// Chore Catalog card). Since chores are shared across children, this
// immediately changes what every assignment of it displays - there's no
// per-child copy to update separately.
func (a *App) UpdateChore(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if chore, err := a.Chores.GetByID(r.Context(), id); err == models.ErrNotFound {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if chore.BootstrapManaged {
		http.Error(w, "this chore is managed by chores.json config and can't be edited here", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if err := a.Chores.Update(r.Context(), id, name, r.FormValue("description")); err != nil {
		logging.Errorf("parent: updating chore id=%d: %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: updated chore id=%d (%q)", id, name)
	a.respondAfterMutation(w, r, "parent/_chore_catalog", "parent/_chore_defs_oob")
}

// DeactivateChore removes a chore from the catalog (the "Delete" button) and
// cascades to deactivate every assignment of it too (see
// models.ChoreStore.Deactivate) - so it also disappears from every child's
// Chores card, not just the catalog list.
func (a *App) DeactivateChore(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if chore, err := a.Chores.GetByID(r.Context(), id); err == models.ErrNotFound {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if chore.BootstrapManaged {
		http.Error(w, "this chore is managed by chores.json config and can't be deleted here", http.StatusForbidden)
		return
	}
	if err := a.Chores.Deactivate(r.Context(), id); err != nil {
		logging.Errorf("parent: deactivating chore id=%d: %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: deactivated chore id=%d", id)
	a.respondAfterMutation(w, r, "parent/_chore_catalog", "parent/_chore_defs_oob")
}

// --- Approvals from the logged-in dashboard (in addition to the email links) ---

func (a *App) ParentDecideChore(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	approve := r.FormValue("decision") == "approve"

	user := auth.UserFromContext(r.Context())
	parentID := 0
	if user != nil {
		parentID = user.ID
	}

	if err := a.ChoreInstances.Decide(r.Context(), id, approve, parentID); err != nil && err != models.ErrInvalidTransition {
		logging.Errorf("parent: deciding chore instance id=%d: %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: chore instance id=%d decided (approve=%v) by user id=%d", id, approve, parentID)
	a.respondAfterMutation(w, r, "parent/_pending_approvals")
}

func (a *App) ParentResetRejectedChore(w http.ResponseWriter, r *http.Request) {
	id, err := parseInt(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := a.ChoreInstances.ResetRejected(r.Context(), id); err != nil && err != models.ErrInvalidTransition {
		logging.Errorf("parent: resetting rejected chore instance id=%d: %v", id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logging.Infof("parent: chore instance id=%d manually reset to incomplete", id)
	http.Redirect(w, r, "/parent", http.StatusSeeOther)
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func startOfWeek(t time.Time) time.Time {
	// Treat Sunday as the start of the week to match the days_of_week bitmask
	// convention (bit 0 = Sunday). Change here if you'd prefer Monday starts.
	daysSinceSunday := int(t.Weekday())
	d := t.AddDate(0, 0, -daysSinceSunday)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
}
