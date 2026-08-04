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
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/weather"
)

// --- View models for templates ---

type dayGroup struct {
	Label  string
	Events []models.Event
}

type childChoreColumn struct {
	Name      string
	Color     string
	AvatarURL string
	Chores    []models.ChoreInstance
}

type kioskViewData struct {
	AppTitle       string
	TodayEvents    []models.Event
	DayGroups      []dayGroup
	ChoresByChild  []childChoreColumn
	ParentLoggedIn bool
	Weather        weatherViewData
	Plugins        []pluginNavItem
}

// kioskIndexData is the top-level kiosk/index page's template data. Which
// view (classic 3-column vs. 5-day) seeds #kiosk-view - and therefore which
// concrete type .Home holds - is decided once per page load from the
// kiosk_layout setting; see KioskIndex.
type kioskIndexData struct {
	AppTitle string
	// HomeIsWeek picks which template kiosk/index.html embeds into
	// #kiosk-view - "kiosk/_week" (with .Home holding *kioskWeekViewData) or
	// "kiosk/_home" (with .Home holding *kioskViewData).
	HomeIsWeek     bool
	Home           any
	ParentLoggedIn bool
	Weather        weatherViewData
	Plugins        []pluginNavItem
}

// KioskIndex renders the full kiosk page. No authentication — this is the
// always-on wall display, per the project requirements.
//
// The kiosk_layout setting is read once here, at page-load time, to decide
// which view is "Home". This is a deliberate simplification: a settings
// change doesn't hot-swap the already-rendered nav bar without a page
// reload/refresh - there's no existing precedent in this app for reactive
// settings, and kiosk devices already reload periodically in practice.
func (a *App) KioskIndex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	layout, err := a.Settings.Get(ctx, settingKioskLayout, kioskLayoutWeekly)
	if err != nil {
		http.Error(w, "failed to load dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}

	idata := kioskIndexData{HomeIsWeek: layout == kioskLayoutWeekly}
	if idata.HomeIsWeek {
		data, err := a.buildKioskWeekViewData(r)
		if err != nil {
			http.Error(w, "failed to load dashboard: "+err.Error(), http.StatusInternalServerError)
			return
		}
		idata.Home = data
		idata.AppTitle, idata.ParentLoggedIn, idata.Weather, idata.Plugins = data.AppTitle, data.ParentLoggedIn, data.Weather, data.Plugins
	} else {
		data, err := a.buildKioskViewData(r)
		if err != nil {
			http.Error(w, "failed to load dashboard: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// The plugin nav bar only needs to be built for the full page load -
		// the home/agenda/calendar/chores fragments never render .Plugins.
		data.Plugins = a.buildPluginNavItems(ctx)
		idata.Home = data
		idata.AppTitle, idata.ParentLoggedIn, idata.Weather, idata.Plugins = data.AppTitle, data.ParentLoggedIn, data.Weather, data.Plugins
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.Templates.ExecuteTemplate(w, "kiosk/index", idata); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// KioskFragmentHome backs the "Home" nav button - swaps #kiosk-view back to
// the agenda/calendar/chores grid (see kiosk/_home.html).
func (a *App) KioskFragmentHome(w http.ResponseWriter, r *http.Request) {
	data, err := a.buildKioskViewData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderFragment(w, "kiosk/_home", data)
}

// KioskFragmentAgenda, KioskFragmentCalendar, KioskFragmentChores back the
// JS-driven seamless background refresh (see web/static/js/kiosk.js) — each
// returns just the inner HTML for one panel.
func (a *App) KioskFragmentAgenda(w http.ResponseWriter, r *http.Request) {
	data, err := a.buildKioskViewData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderFragment(w, "kiosk/_agenda", data)
}

func (a *App) KioskFragmentCalendar(w http.ResponseWriter, r *http.Request) {
	data, err := a.buildKioskViewData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderFragment(w, "kiosk/_calendar", data)
}

func (a *App) KioskFragmentChores(w http.ResponseWriter, r *http.Request) {
	data, err := a.buildKioskViewData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderFragment(w, "kiosk/_chores", data)
}

// eventDetailData wraps a models.Event with whether the current request is
// from a signed-in parent, so kiosk/_event_detail.html can hide any
// EventAction with RequiresParent set from an anonymous kiosk view while
// still showing it to a parent browsing the same page (e.g. on a laptop,
// already logged into /parent - see auth.SessionManager.LoadUser, which is
// mounted ahead of the kiosk routes and never blocks an anonymous request,
// just optionally attaches a *models.User to context if a valid parent
// session cookie is present).
type eventDetailData struct {
	models.Event
	IsParent       bool
	VisibleActions []models.EventAction
}

// KioskEventDetail returns the detail popup fragment for one calendar event,
// backing the "tap an event for the full card" feature. Unlike the list
// queries (ListToday/ListInWindow), this fetches description/organizer/
// attendees/attachments, which aren't needed for the list view and would
// otherwise be pulled on every 60s poll for no reason.
func (a *App) KioskEventDetail(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r, "id")
	if err != nil {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}

	event, err := a.Events.GetByID(r.Context(), id)
	if err != nil {
		if err == models.ErrNotFound {
			http.Error(w, "event not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	user := auth.UserFromContext(r.Context())
	isParent := user != nil && user.Role == models.RoleParent
	visible := make([]models.EventAction, 0, len(event.Actions))
	for _, act := range event.Actions {
		if !act.RequiresParent || isParent {
			visible = append(visible, act)
		}
	}
	a.renderFragment(w, "kiosk/_event_detail", eventDetailData{
		Event:          *event,
		IsParent:       isParent,
		VisibleActions: visible,
	})
}

func (a *App) renderFragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.Templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// KioskCompleteChore handles a child tapping their chore on the kiosk. No auth
// required by design (kids don't log in) — it only ever moves a SPECIFIC chore
// instance from incomplete -> pending_approval, which is a narrow, low-risk
// surface even without authentication on a trusted home network.
// KioskCompleteChore handles a child tapping their chore on the kiosk. It
// responds with the refreshed chores fragment (rather than a bare 200) so the
// htmx hx-post on the tapped <li> (see web/templates/kiosk/_chores.html) can
// swap #chores-content in place without waiting for the next 60s poll.
func (a *App) KioskCompleteChore(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r, "id")
	if err != nil {
		http.Error(w, "invalid chore id", http.StatusBadRequest)
		return
	}
	logging.Debugf("kiosk: chore tap received for instance id=%d", id)

	status, err := a.ChoreInstances.MarkComplete(r.Context(), id)
	if err != nil {
		if err != models.ErrInvalidTransition {
			logging.Errorf("kiosk: marking chore instance id=%d complete: %v", id, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Already pending/approved/etc — not an error from the kiosk's
		// perspective, just a no-op (e.g. double tap).
		logging.Debugf("kiosk: chore instance id=%d was not in a tappable state (double tap?), ignoring", id)
	} else if status == models.StatusPendingApproval {
		logging.Infof("kiosk: chore instance id=%d marked complete, notifying parents", id)
		go a.notifyParentsOfCompletion(r.Context(), id) // see approval.go - fire-and-forget so the kiosk stays snappy
	} else {
		// Assigned to a parent (informational only) - MarkComplete already
		// jumped straight to 'approved', no approval step or email needed.
		logging.Infof("kiosk: chore instance id=%d (parent-assigned, informational) marked approved", id)
	}

	// Chores tapped from the 5-day view carry ?view=week&day=... (see
	// kiosk/_week_day_chores.html) so only that one day's chore column gets
	// re-rendered, instead of rebuilding the whole 5-day kioskWeekViewData
	// for a single-row change.
	if r.URL.Query().Get("view") == kioskWeekViewQueryValue {
		a.renderWeekDayChoresFragment(w, r, r.URL.Query().Get("day"))
		return
	}

	data, err := a.buildKioskViewData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.renderFragment(w, "kiosk/_chores", data)
}

func (a *App) buildKioskViewData(r *http.Request) (*kioskViewData, error) {
	ctx := r.Context()
	today := time.Now()

	if err := a.ChoreInstances.EnsureForDate(ctx, today); err != nil {
		return nil, err
	}

	todayEvents, err := a.Events.ListToday(ctx)
	if err != nil {
		return nil, err
	}

	windowEvents, err := a.Events.ListInWindow(ctx, a.Cfg.CalendarWindowDays)
	if err != nil {
		return nil, err
	}

	choreInstances, err := a.ChoreInstances.ListActiveForDate(ctx, today)
	if err != nil {
		return nil, err
	}

	// Avatar presence is resolved here, from the already-loaded user list,
	// rather than adding avatar columns to ChoreInstance's own JOIN queries -
	// keeps those queries focused on chore state, not user display details.
	avatarByUserID, err := a.avatarURLsByUserID(ctx)
	if err != nil {
		return nil, err
	}

	user := auth.UserFromContext(ctx)

	appTitle, err := a.Settings.Get(ctx, "app_title", a.Cfg.AppTitle)
	if err != nil {
		return nil, err
	}

	return &kioskViewData{
		AppTitle:       appTitle,
		TodayEvents:    todayEvents,
		DayGroups:      groupEventsByDay(windowEvents),
		ChoresByChild:  groupChoresByChild(choreInstances, avatarByUserID),
		ParentLoggedIn: user != nil && user.Role == models.RoleParent,
		Weather:        a.currentWeatherViewData(),
	}, nil
}

func (a *App) currentWeatherViewData() weatherViewData {
	forecast, ok := a.Weather.Get()
	if !ok {
		return weatherViewData{Available: false}
	}
	code := weather.DescribeCode(forecast.CurrentCode)
	return weatherViewData{
		Available:   true,
		Icon:        code.Icon,
		Description: code.Description,
		CurrentTemp: forecast.CurrentTemp,
	}
}

func groupEventsByDay(events []models.Event) []dayGroup {
	groups := map[string]*dayGroup{}
	var order []string

	for _, e := range events {
		key := e.StartsAt.Format("2006-01-02")
		g, ok := groups[key]
		if !ok {
			g = &dayGroup{Label: dayLabel(e.StartsAt)}
			groups[key] = g
			order = append(order, key)
		}
		g.Events = append(g.Events, e)
	}

	sort.Strings(order)
	out := make([]dayGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	return out
}

func dayLabel(t time.Time) string {
	now := time.Now()
	if sameDay(t, now) {
		return "Today"
	}
	if sameDay(t, now.AddDate(0, 0, 1)) {
		return "Tomorrow"
	}
	return t.Format("Monday, Jan 2")
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

func groupChoresByChild(instances []models.ChoreInstance, avatarByUserID map[int]string) []childChoreColumn {
	columns := map[int]*childChoreColumn{}
	var order []int

	for _, ci := range instances {
		col, ok := columns[ci.ChildID]
		if !ok {
			col = &childChoreColumn{Name: ci.ChildName, Color: ci.ChildColor, AvatarURL: avatarByUserID[ci.ChildID]}
			columns[ci.ChildID] = col
			order = append(order, ci.ChildID)
		}
		col.Chores = append(col.Chores, ci)
	}

	sort.Ints(order)
	out := make([]childChoreColumn, 0, len(order))
	for _, id := range order {
		out = append(out, *columns[id])
	}
	return out
}

// avatarURLsByUserID builds a childID/parentID -> avatar URL lookup from the
// full user list, for views (like the kiosk chore tracker and the dashboard's
// pending-approvals list) that need to show an avatar next to a name without
// adding avatar columns to their own JOIN queries.
func (a *App) avatarURLsByUserID(ctx context.Context) (map[int]string, error) {
	users, err := a.Users.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]string, len(users))
	for _, u := range users {
		if url := u.AvatarURL(); url != "" {
			byID[u.ID] = url
		}
	}
	return byID, nil
}

func parseIDParam(r *http.Request, name string) (int, error) {
	return parseInt(chi.URLParam(r, name))
}

// --- Weather ---

type weatherViewData struct {
	Available   bool
	Icon        string
	Description string
	CurrentTemp float64
}

type hourlyPoint struct {
	Label             string
	Icon              string
	Temp              float64
	PrecipProbability int
}

type dailyPoint struct {
	Label             string
	Icon              string
	TempMax           float64
	TempMin           float64
	PrecipProbability int
}

type weatherDetailViewData struct {
	Available     bool
	Icon          string
	Description   string
	CurrentTemp   float64
	Hourly        []hourlyPoint
	Daily         []dailyPoint
	RadarEmbedURL string
}

// KioskFragmentWeather backs the header widget's 60s poll. It only ever reads
// the in-memory cache the scheduler maintains (see internal/scheduler's
// runWeatherRefresh) - it never calls Open-Meteo directly, so polling this
// endpoint is cheap regardless of how often the kiosk refreshes it.
func (a *App) KioskFragmentWeather(w http.ResponseWriter, r *http.Request) {
	a.renderFragment(w, "kiosk/_weather", a.currentWeatherViewData())
}

// KioskWeatherPage backs the "Weather" nav button and the header quick-glance
// widget's click-through: a full-screen forecast (current/hourly/7-day) plus
// an embedded RainViewer radar map centered on the configured location.
func (a *App) KioskWeatherPage(w http.ResponseWriter, r *http.Request) {
	forecast, ok := a.Weather.Get()
	if !ok {
		a.renderFragment(w, "kiosk/_weather_page", weatherDetailViewData{Available: false})
		return
	}

	code := weather.DescribeCode(forecast.CurrentCode)
	data := weatherDetailViewData{
		Available:   true,
		Icon:        code.Icon,
		Description: code.Description,
		CurrentTemp: forecast.CurrentTemp,
	}
	for _, h := range forecast.Hourly {
		hc := weather.DescribeCode(h.Code)
		data.Hourly = append(data.Hourly, hourlyPoint{
			Label:             h.Time.Format("3 PM"),
			Icon:              hc.Icon,
			Temp:              h.Temp,
			PrecipProbability: h.PrecipProbability,
		})
	}
	for _, d := range forecast.Daily {
		dc := weather.DescribeCode(d.Code)
		data.Daily = append(data.Daily, dailyPoint{
			Label:             d.Date.Format("Mon"),
			Icon:              dc.Icon,
			TempMax:           d.TempMax,
			TempMin:           d.TempMin,
			PrecipProbability: d.PrecipProbability,
		})
	}

	if lat, lon, _, ok, err := weather.LoadLocation(r.Context(), a.Settings); err == nil && ok {
		radarZoom, err := weather.LoadRadarZoom(r.Context(), a.Settings)
		if err != nil {
			logging.Errorf("kiosk: loading radar zoom setting: %v", err)
			radarZoom = weather.DefaultRadarZoom
		}
		data.RadarEmbedURL = fmt.Sprintf("https://www.rainviewer.com/map.html?loc=%.4f,%.4f,%d&oFa=0&oC=1&oU=0&oCS=1&c=3&o=83&lm=0&layer=radar&sm=1&sn=1", lat, lon, radarZoom)
	}

	a.renderFragment(w, "kiosk/_weather_page", data)
}
