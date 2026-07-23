package handlers

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/weather"
)

// refreshWeatherAsync re-fetches the forecast for whatever location is
// currently configured, in the background, so a parent saving a new location
// doesn't have to wait for the scheduler's next tick to see it reflected on
// the kiosk. Mirrors syncAccountAsync's pattern (internal/handlers/sync.go).
func (a *App) refreshWeatherAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		lat, lon, units, ok, err := weather.LoadLocation(ctx, a.Settings)
		if err != nil {
			logging.Errorf("on-demand weather refresh: loading location settings: %v", err)
			return
		}
		if !ok {
			logging.Debugf("on-demand weather refresh: no location configured, skipping")
			return
		}

		forecast, err := weather.FetchForecast(ctx, lat, lon, units)
		if err != nil {
			logging.Errorf("on-demand weather refresh: fetching forecast: %v", err)
			return
		}
		a.Weather.Set(forecast)
		logging.Infof("on-demand weather refresh: forecast updated (lat=%.4f, lon=%.4f)", lat, lon)
	}()
}

// BootstrapWeatherLocation seeds the weather location from config-provided
// env vars (WEATHER_LOCATION and/or WEATHER_LAT/WEATHER_LON, plus optional
// WEATHER_UNITS) on first startup only. Unlike CALENDAR_ACCOUNTS bootstrap
// (which reconciles on every restart), a location that's already
// configured - whether from a prior bootstrap run or a parent setting it
// via the dashboard - is left untouched, so a parent's choice sticks rather
// than getting fought over on every pod restart. locationQuery is geocoded
// only when explicit coordinates aren't supplied; supplying both prefers the
// coordinates (no network dependency at startup) and uses locationQuery only
// as the display name.
func (a *App) BootstrapWeatherLocation(ctx context.Context, locationQuery string, lat, lon float64, hasCoords bool, units string) {
	_, _, _, ok, err := weather.LoadLocation(ctx, a.Settings)
	if err != nil {
		logging.Errorf("bootstrap: checking existing weather location: %v", err)
		return
	}
	if ok {
		logging.Debugf("bootstrap: weather location already configured, skipping WEATHER_LOCATION/WEATHER_LAT/WEATHER_LON")
		return
	}

	name := locationQuery
	if !hasCoords {
		loc, err := weather.Geocode(ctx, locationQuery)
		if err != nil {
			logging.Errorf("bootstrap: geocoding WEATHER_LOCATION %q failed: %v", locationQuery, err)
			return
		}
		name, lat, lon = loc.Name, loc.Lat, loc.Lon
	} else if name == "" {
		name = fmt.Sprintf("%.4f, %.4f", lat, lon)
	}

	if units != string(weather.UnitsMetric) {
		units = string(weather.UnitsImperial)
	}

	if err := a.Settings.Set(ctx, weather.SettingLocationName, name); err != nil {
		logging.Errorf("bootstrap: setting weather location name: %v", err)
		return
	}
	if err := a.Settings.Set(ctx, weather.SettingLat, strconv.FormatFloat(lat, 'f', -1, 64)); err != nil {
		logging.Errorf("bootstrap: setting weather lat: %v", err)
		return
	}
	if err := a.Settings.Set(ctx, weather.SettingLon, strconv.FormatFloat(lon, 'f', -1, 64)); err != nil {
		logging.Errorf("bootstrap: setting weather lon: %v", err)
		return
	}
	if err := a.Settings.Set(ctx, weather.SettingUnits, units); err != nil {
		logging.Errorf("bootstrap: setting weather units: %v", err)
		return
	}

	logging.Infof("bootstrap: seeded weather location %q from config", name)
	a.refreshWeatherAsync()
}
