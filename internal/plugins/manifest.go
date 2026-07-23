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

// Package plugins is the host-side client for hhq's external-process plugin
// contract: each plugin is an independently-run HTTP service implementing
// GET /manifest, GET /view, GET /events, GET+POST /settings, and
// GET /healthz. All plugin-specific logic lives in the plugin process
// itself - this package only knows how to talk the shared protocol, never
// anything about what a particular plugin does.
//
// hhq treats a registered plugin's HTTP responses as trusted HTML, not
// sanitized user input - only register plugins you wrote or trust as much
// as hhq itself (see the Plugins card on the parent dashboard and the
// project README for more on this trust boundary).
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// manifestTimeout bounds the GET /manifest fetch performed at registration
// time and periodically thereafter - generous since it's not on the hot
// path of any page render.
const manifestTimeout = 10 * time.Second

// ViewManifest describes whether/how a plugin wants a kiosk nav button and
// full-screen view - see the Manifest.View field. Icon is trusted inline SVG
// markup, inlined directly into the kiosk nav button (same trust boundary as
// the HTML returned by GET /view - see the package doc comment); a plugin
// that leaves it blank gets a generic default icon (see
// kiosk/_plugin_default_icon.html).
type ViewManifest struct {
	Enabled bool   `json:"enabled"`
	Label   string `json:"label"`
	Icon    string `json:"icon"`
}

// Manifest is the JSON shape returned by a plugin's GET /manifest.
type Manifest struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Version        string       `json:"version"`
	View           ViewManifest `json:"view"`
	ProvidesEvents bool         `json:"provides_events"`
}

// FetchManifest calls GET {baseURL}/manifest and decodes the response.
func FetchManifest(ctx context.Context, baseURL, token string) (*Manifest, error) {
	ctx, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/manifest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching manifest: unexpected status %d", resp.StatusCode)
	}

	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("decoding manifest: %w", err)
	}
	return &m, nil
}
