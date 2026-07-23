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

package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// viewTimeout bounds the GET /view fetch, which happens inline during a
// kiosk request (a child/parent is tapping a nav button and waiting) - kept
// short so a hung plugin process can't stall the kiosk.
const viewTimeout = 3 * time.Second

// eventsTimeout bounds the GET /events fetch, which only ever happens in the
// background scheduler (see internal/scheduler/plugin_sync.go), so it can
// afford to be more patient than the view fetch.
const eventsTimeout = 10 * time.Second

// healthzTimeout bounds the GET /healthz check.
const healthzTimeout = 3 * time.Second

// FetchView calls GET {baseURL}/view and returns the raw HTML fragment
// body, to be inlined server-side into the kiosk's full-screen content
// region (see internal/handlers/plugins.go's KioskPluginView). The response
// is never forwarded to the browser directly - only ever fetched
// server-to-server.
func FetchView(ctx context.Context, baseURL, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, viewTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/view", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching view: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching view: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading view response: %w", err)
	}
	return string(body), nil
}

// EventAction is one action button a plugin wants shown on an event's kiosk
// detail popup (e.g. "Mark Paid"). ID must match a route the plugin serves
// at POST {baseURL}/actions/{id} - see PostAction.
type EventAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// RequiresParent, when true, means this action should only be shown/
	// invokable by a signed-in parent (e.g. "Mark Paid") - not by an
	// unauthenticated kiosk tap, which could happen accidentally (a child
	// tapping around the screen). See models.EventAction and
	// internal/handlers/kiosk.go's KioskEventDetail/KioskEventAction, which
	// enforce this both in the rendered template and server-side.
	RequiresParent bool `json:"requires_parent,omitempty"`
}

// Event is one synthetic calendar event as returned by a plugin's
// GET /events - shaped like calendar_events_cache's columns, minus
// organizer/attendees/attachments (not needed for a synthetic source; can be
// added later if a plugin needs them).
type Event struct {
	UID         string        `json:"uid"`
	Summary     string        `json:"summary"`
	Location    string        `json:"location"`
	Description string        `json:"description"`
	StartsAt    time.Time     `json:"starts_at"`
	EndsAt      time.Time     `json:"ends_at"`
	AllDay      bool          `json:"all_day"`
	Actions     []EventAction `json:"actions,omitempty"`
}

type eventsResponse struct {
	Events []Event `json:"events"`
}

// FetchEvents calls GET {baseURL}/events?from=...&to=... (dates formatted
// YYYY-MM-DD) and returns the plugin's synthetic events for that window.
func FetchEvents(ctx context.Context, baseURL, token string, from, to time.Time) ([]Event, error) {
	ctx, cancel := context.WithTimeout(ctx, eventsTimeout)
	defer cancel()

	url := fmt.Sprintf("%s/events?from=%s&to=%s", baseURL, from.Format("2006-01-02"), to.Format("2006-01-02"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching events: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching events: unexpected status %d", resp.StatusCode)
	}

	var decoded eventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decoding events response: %w", err)
	}
	return decoded.Events, nil
}

// actionTimeout bounds the POST /actions/{id} call, which happens inline
// during a kiosk request (the child is tapping a button and waiting), so it
// needs to fail fast rather than hang the kiosk.
const actionTimeout = 10 * time.Second

// PostAction calls POST {baseURL}/actions/{actionID} with a JSON body of
// {"uid": uid}, identifying which synthetic event the action applies to.
// The plugin is expected to perform its side effect (e.g. marking a bill
// paid) and respond 2xx on success. hhq only ever calls this for an
// actionID it already found listed on the event's own Actions - see
// internal/handlers/kiosk.go's KioskEventAction.
func PostAction(ctx context.Context, baseURL, token, actionID, uid string) error {
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()

	body, err := json.Marshal(struct {
		UID string `json:"uid"`
	}{UID: uid})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/actions/"+actionID, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("posting action %s: %w", actionID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("posting action %s: unexpected status %d", actionID, resp.StatusCode)
	}
	return nil
}

// Healthz calls GET {baseURL}/healthz and returns nil if the plugin
// responded with any 2xx status.
func Healthz(ctx context.Context, baseURL string) error {
	ctx, cancel := context.WithTimeout(ctx, healthzTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health check: unexpected status %d", resp.StatusCode)
	}
	return nil
}
