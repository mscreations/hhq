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
