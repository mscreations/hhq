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

package main

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/weather"
)

// TestKioskEventDetailReturnsServerErrorOnGenericDBFailure covers
// KioskEventDetail's generic (non-ErrNotFound) GetByID-error branch.
func TestKioskEventDetailReturnsServerErrorOnGenericDBFailure(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Events = &models.EventStore{DB: brokenDB(t)}

	resp, err := ts.Client.Get(ts.URL + "/kiosk/events/1")
	if err != nil {
		t.Fatalf("GET /kiosk/events/1: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestKioskEventDetailFiltersActionsByParentStatus covers KioskEventDetail's
// action-visibility loop - both a plugin action visible to everyone and one
// gated behind RequiresParent, viewed both anonymously and while logged in
// as a parent (existing tests never populate event.Actions at all).
func TestKioskEventDetailFiltersActionsByParentStatus(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()

	accountID, err := ts.App.CalendarAccounts.Create(ctx, models.CalendarAccount{
		Name: "Acct", Provider: models.ProviderGeneric, EncryptedPassword: []byte("x"),
	})
	if err != nil {
		t.Fatalf("CalendarAccounts.Create: %v", err)
	}
	calID, err := ts.App.Calendars.UpsertDiscovered(ctx, accountID, "/path/", "Home", "#3B82F6")
	if err != nil {
		t.Fatalf("UpsertDiscovered: %v", err)
	}

	now := time.Now()
	if err := ts.App.Events.Upsert(ctx, models.Event{
		CalendarID: calID,
		UID:        "detail-actions@example.com",
		Summary:    "Bill due",
		Actions: []models.EventAction{
			{ID: "pay", Label: "Mark Paid", RequiresParent: false},
			{ID: "approve", Label: "Approve Refund", RequiresParent: true},
		},
		StartsAt: now,
		EndsAt:   now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Events.Upsert: %v", err)
	}
	events, err := ts.App.Events.ListToday(ctx)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListToday: events=%+v err=%v", events, err)
	}

	// Anonymous: only the non-parent-gated action should render.
	resp, err := ts.Client.Get(ts.URL + "/kiosk/events/" + strconv.Itoa(events[0].ID))
	if err != nil {
		t.Fatalf("GET /kiosk/events: %v", err)
	}
	body := readBody(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if !strings.Contains(body, "Mark Paid") {
		t.Errorf("expected the non-gated action to be visible anonymously, body: %s", body)
	}
	if strings.Contains(body, "Approve Refund") {
		t.Errorf("expected the parent-gated action to be hidden anonymously, body: %s", body)
	}

	// Logged in as a parent: both actions should render.
	ts.login(t, "parent-detail@example.com", "hunter22")
	resp2, err := ts.Client.Get(ts.URL + "/kiosk/events/" + strconv.Itoa(events[0].ID))
	if err != nil {
		t.Fatalf("GET /kiosk/events (as parent): %v", err)
	}
	body2 := readBody(t, resp2)
	resp2.Body.Close()
	if !strings.Contains(body2, "Approve Refund") {
		t.Errorf("expected the parent-gated action to be visible when logged in, body: %s", body2)
	}
}

// TestKioskCompleteChoreReturnsServerErrorOnGenericMarkCompleteFailure
// covers KioskCompleteChore's generic (non-ErrInvalidTransition)
// MarkComplete-error branch.
func TestKioskCompleteChoreReturnsServerErrorOnGenericMarkCompleteFailure(t *testing.T) {
	ts := newTestServer(t)
	ts.App.ChoreInstances = &models.ChoreInstanceStore{DB: brokenDB(t)}

	resp, err := ts.Client.Post(ts.URL+"/kiosk/chores/1/complete", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST /kiosk/chores/1/complete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestKioskFragmentAgendaReturnsServerErrorOnListTodayFailure covers
// buildKioskViewData's ListToday-error branch (the first of its two Events
// calls, so isolable by fully breaking the Events store).
func TestKioskFragmentAgendaReturnsServerErrorOnListTodayFailure(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Events = &models.EventStore{DB: brokenDB(t)}

	resp, err := ts.Client.Get(ts.URL + "/kiosk/fragments/agenda")
	if err != nil {
		t.Fatalf("GET /kiosk/fragments/agenda: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestKioskIndexReturnsServerErrorOnAppTitleSettingFailure covers
// buildKioskViewData's Settings.Get (app_title) error branch, isolated by
// breaking only the Settings store - ChoreInstances/Events (called earlier
// in the same function) stay on the real, working DB.
func TestKioskIndexReturnsServerErrorOnAppTitleSettingFailure(t *testing.T) {
	ts := newTestServer(t)
	ts.App.Settings = &models.SettingsStore{DB: brokenDB(t)}

	resp, err := ts.Client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestKioskWeatherPageIncludesRadarEmbedURLWhenLocationConfigured covers
// KioskWeatherPage's weather.LoadLocation-succeeds branch (building
// RadarEmbedURL) - no existing test configures a location, so this whole
// branch (and the fmt.Sprintf building the URL) had zero coverage.
func TestKioskWeatherPageIncludesRadarEmbedURLWhenLocationConfigured(t *testing.T) {
	ts := newTestServer(t)
	ctx := t.Context()
	if err := ts.App.Settings.Set(ctx, "weather_lat", "40.7128"); err != nil {
		t.Fatalf("Settings.Set lat: %v", err)
	}
	if err := ts.App.Settings.Set(ctx, "weather_lon", "-74.0060"); err != nil {
		t.Fatalf("Settings.Set lon: %v", err)
	}
	ts.App.Weather.Set(&weather.Forecast{CurrentTemp: 72, CurrentCode: 0})

	resp, err := ts.Client.Get(ts.URL + "/kiosk/weather/page")
	if err != nil {
		t.Fatalf("GET /kiosk/weather/page: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "rainviewer.com") {
		t.Errorf("expected the radar embed URL to be present once a location is configured, body: %s", body)
	}
}
