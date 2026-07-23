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

package weather

import (
	"context"
	"strconv"
	"sync"

	"github.com/mscreations/hhq/internal/models"
)

// Cache holds the most recently fetched Forecast in memory, refreshed
// periodically by the scheduler. There's no persisted table for this -
// weather data is cheap to refetch and doesn't need to survive a restart
// (same "single replica only" assumption already documented for the rest of
// this app's background jobs).
type Cache struct {
	mu       sync.RWMutex
	forecast *Forecast
}

// Get returns the cached forecast, or (nil, false) if nothing has been
// fetched yet (e.g. no location configured, or the app just started).
func (c *Cache) Get() (*Forecast, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.forecast == nil {
		return nil, false
	}
	return c.forecast, true
}

// Set stores the latest fetched forecast.
func (c *Cache) Set(f *Forecast) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forecast = f
}

// Clear drops any cached forecast, e.g. when a location is no longer configured.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forecast = nil
}

// Settings keys shared by the scheduler (reading, to refresh the forecast)
// and the parent handlers (writing, when a parent saves a new location).
const (
	SettingLocationName = "weather_location_name"
	SettingLat          = "weather_lat"
	SettingLon          = "weather_lon"
	SettingUnits        = "weather_units"
	SettingRadarZoom    = "weather_radar_zoom"
)

// DefaultRadarZoom is used when the parent hasn't configured a radar zoom
// level yet - close enough to see local precipitation without needing
// interactive pan/zoom controls on a kiosk display.
const DefaultRadarZoom = 10

// LoadRadarZoom reads the configured RainViewer radar zoom level out of the
// settings store, falling back to DefaultRadarZoom if unset or invalid.
func LoadRadarZoom(ctx context.Context, settings *models.SettingsStore) (int, error) {
	zoomStr, err := settings.Get(ctx, SettingRadarZoom, "")
	if err != nil {
		return 0, err
	}
	if zoomStr == "" {
		return DefaultRadarZoom, nil
	}
	zoom, err := strconv.Atoi(zoomStr)
	if err != nil {
		return DefaultRadarZoom, nil
	}
	return zoom, nil
}

// LoadLocation reads the configured location/units out of the settings
// store. ok is false if no location has been configured yet (nothing to
// refresh), not on any other error.
func LoadLocation(ctx context.Context, settings *models.SettingsStore) (lat, lon float64, units Units, ok bool, err error) {
	latStr, err := settings.Get(ctx, SettingLat, "")
	if err != nil {
		return 0, 0, "", false, err
	}
	lonStr, err := settings.Get(ctx, SettingLon, "")
	if err != nil {
		return 0, 0, "", false, err
	}
	if latStr == "" || lonStr == "" {
		return 0, 0, "", false, nil
	}

	lat, err = strconv.ParseFloat(latStr, 64)
	if err != nil {
		return 0, 0, "", false, err
	}
	lon, err = strconv.ParseFloat(lonStr, 64)
	if err != nil {
		return 0, 0, "", false, err
	}

	unitsStr, err := settings.Get(ctx, SettingUnits, string(UnitsImperial))
	if err != nil {
		return 0, 0, "", false, err
	}
	units = UnitsImperial
	if unitsStr == string(UnitsMetric) {
		units = UnitsMetric
	}

	return lat, lon, units, true, nil
}
