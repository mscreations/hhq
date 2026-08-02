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
