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

package release

import "sync"

// Cache holds the most recently fetched Release in memory, refreshed
// periodically by the scheduler. No persisted table - same "cheap to
// refetch, doesn't need to survive a restart" rationale as weather.Cache.
type Cache struct {
	mu      sync.RWMutex
	release *Release
}

// Get returns the cached release, or (nil, false) if nothing has been
// fetched yet (e.g. the app just started, or GitHub was unreachable).
func (c *Cache) Get() (*Release, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.release == nil {
		return nil, false
	}
	return c.release, true
}

// Set stores the latest fetched release.
func (c *Cache) Set(r *Release) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.release = r
}

// PluginCache holds the most recently checked Release per plugin ID, the
// same in-memory/no-persistence rationale as Cache - refreshed periodically
// by the scheduler's checkPluginUpdates, driving the parent dashboard's
// per-plugin update-available icon.
type PluginCache struct {
	mu       sync.RWMutex
	releases map[string]*Release
}

// Get returns the cached release for plugin id, or (nil, false) if it
// hasn't been checked yet (e.g. no repo_url configured, or the app just
// started).
func (c *PluginCache) Get(id string) (*Release, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.releases[id]
	return r, ok
}

// Set stores the latest checked release for plugin id.
func (c *PluginCache) Set(id string, r *Release) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.releases == nil {
		c.releases = make(map[string]*Release)
	}
	c.releases[id] = r
}
