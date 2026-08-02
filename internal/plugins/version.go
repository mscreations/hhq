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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// versionTimeout bounds the GET /version fetch performed periodically by the
// scheduler (see internal/scheduler/plugin_sync.go) - short since it's a
// cheap, infrequent background poll, not on the hot path of any page render.
const versionTimeout = 5 * time.Second

// VersionInfo is the JSON shape returned by a plugin's GET /version - the
// plugin checks its own repo for a newer published version and reports the
// result, so hhq never needs to know a plugin's repo or talk to GitHub on
// its behalf.
type VersionInfo struct {
	Version          string `json:"version"`
	UpgradeAvailable bool   `json:"upgradeAvailable"`
	UpgradeVersion   string `json:"upgradeVersion"`
	Changelog        string `json:"changelog"`
	Channel          string `json:"channel"`
}

// FetchVersion calls GET {baseURL}/version, unauthenticated - matching
// /healthz, this is checked well before (and independent of) a plugin having
// self-registered a bearer token.
func FetchVersion(ctx context.Context, baseURL string) (*VersionInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/version", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching version: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching version: unexpected status %d", resp.StatusCode)
	}

	var v VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("decoding version response: %w", err)
	}
	return &v, nil
}

// VersionCache holds the most recently fetched VersionInfo per plugin ID -
// in-memory only, refreshed periodically by the scheduler's
// checkPluginVersions, driving the parent dashboard's per-plugin
// update-available icon. No persisted table, same "cheap to refetch,
// doesn't need to survive a restart" rationale as weather.Cache/release.Cache.
type VersionCache struct {
	mu    sync.RWMutex
	infos map[string]*VersionInfo
}

// Get returns the cached version info for plugin id, or (nil, false) if it
// hasn't been checked yet (e.g. the app just started, or the plugin hasn't
// responded to a /version fetch yet).
func (c *VersionCache) Get(id string) (*VersionInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.infos[id]
	return v, ok
}

// Set stores the latest fetched version info for plugin id.
func (c *VersionCache) Set(id string, v *VersionInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.infos == nil {
		c.infos = make(map[string]*VersionInfo)
	}
	c.infos[id] = v
}
