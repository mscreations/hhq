package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
	"github.com/mscreations/hhq/internal/weather"
)

// brokenSettingsDB returns an open-then-closed *sql.DB, mirroring
// cmd/server/router_test.go's brokenDB - kept as a separate local helper
// here since internal/handlers' own tests can't import the cmd/server
// package (and shouldn't need to just for this).
func brokenSettingsDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("pgx", "postgres://broken:broken@127.0.0.1:1/broken?sslmode=disable")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	conn.Close()
	return conn
}

// TestRefreshWeatherAsyncLoadLocationError covers refreshWeatherAsync's
// weather.LoadLocation error branch: a broken Settings store must be logged
// and not populate the cache.
func TestRefreshWeatherAsyncLoadLocationError(t *testing.T) {
	a := &App{
		Settings: &models.SettingsStore{DB: brokenSettingsDB(t)},
		Weather:  &weather.Cache{},
	}

	a.refreshWeatherAsync()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := a.Weather.Get(); ok {
		t.Fatal("expected the weather cache to remain empty when LoadLocation fails")
	}
}

// TestRefreshWeatherAsyncNoLocationConfigured covers refreshWeatherAsync's
// "no location configured, skipping" branch (LoadLocation succeeds with
// ok=false).
func TestRefreshWeatherAsyncNoLocationConfigured(t *testing.T) {
	conn := testutil.RequireDB(t)
	a := &App{
		Settings: &models.SettingsStore{DB: conn},
		Weather:  &weather.Cache{},
	}

	a.refreshWeatherAsync()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := a.Weather.Get(); ok {
		t.Fatal("expected the weather cache to remain empty when no location is configured")
	}
}

// TestRefreshWeatherAsyncFetchForecastError covers refreshWeatherAsync's
// weather.FetchForecast error branch: a location is configured, but the
// (redirected, for determinism) Open-Meteo endpoint fails.
func TestRefreshWeatherAsyncFetchForecastError(t *testing.T) {
	conn := testutil.RequireDB(t)
	settings := &models.SettingsStore{DB: conn}
	ctx := context.Background()
	if err := settings.Set(ctx, weather.SettingLat, "41.85"); err != nil {
		t.Fatalf("Set lat: %v", err)
	}
	if err := settings.Set(ctx, weather.SettingLon, "-87.65"); err != nil {
		t.Fatalf("Set lon: %v", err)
	}

	forecastSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forecast unavailable", http.StatusInternalServerError)
	}))
	t.Cleanup(forecastSrv.Close)
	origForecastURL := weather.ForecastURL
	weather.ForecastURL = forecastSrv.URL
	t.Cleanup(func() { weather.ForecastURL = origForecastURL })

	a := &App{Settings: settings, Weather: &weather.Cache{}}
	a.refreshWeatherAsync()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := a.Weather.Get(); ok {
		t.Fatal("expected the weather cache to remain empty when FetchForecast fails")
	}
}

// --- BootstrapWeatherLocation remaining branches ---

// TestBootstrapWeatherLocationChecksExistingError covers the "checking
// existing weather location" error branch: a broken Settings store must be
// logged and return without attempting to seed anything.
func TestBootstrapWeatherLocationChecksExistingError(t *testing.T) {
	a := &App{
		Settings: &models.SettingsStore{DB: brokenSettingsDB(t)},
		Weather:  &weather.Cache{},
	}

	// Must not panic.
	a.BootstrapWeatherLocation(context.Background(), "Somewhere", 0, 0, false, "")
}

// TestBootstrapWeatherLocationGeocodeFailureReturnsWithoutSeeding covers the
// "geocoding WEATHER_LOCATION failed" error branch.
func TestBootstrapWeatherLocationGeocodeFailureReturnsWithoutSeeding(t *testing.T) {
	conn := testutil.RequireDB(t)
	settings := &models.SettingsStore{DB: conn}

	geocodeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "geocoding failed", http.StatusInternalServerError)
	}))
	t.Cleanup(geocodeSrv.Close)
	origGeocodeURL := weather.GeocodeURL
	weather.GeocodeURL = geocodeSrv.URL
	t.Cleanup(func() { weather.GeocodeURL = origGeocodeURL })

	a := &App{Settings: settings, Weather: &weather.Cache{}}
	a.BootstrapWeatherLocation(context.Background(), "Nowhere", 0, 0, false, "")

	if _, err := settings.Get(context.Background(), weather.SettingLocationName, ""); err != nil {
		t.Fatalf("Get: %v", err)
	} else if name, _ := settings.Get(context.Background(), weather.SettingLocationName, ""); name != "" {
		t.Fatalf("expected no location to be seeded after a geocode failure, got %q", name)
	}
}

// TestBootstrapWeatherLocationCoordsGivenEmptyNameDefaultsToLatLonString
// covers the hasCoords=true, locationQuery=="" branch, where name falls back
// to a formatted "lat, lon" string.
func TestBootstrapWeatherLocationCoordsGivenEmptyNameDefaultsToLatLonString(t *testing.T) {
	conn := testutil.RequireDB(t)
	settings := &models.SettingsStore{DB: conn}
	a := &App{Settings: settings, Weather: &weather.Cache{}}

	a.BootstrapWeatherLocation(context.Background(), "", 41.85, -87.65, true, string(weather.UnitsMetric))

	name, err := settings.Get(context.Background(), weather.SettingLocationName, "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if name != "41.8500, -87.6500" {
		t.Fatalf("weather_location_name = %q, want %q", name, "41.8500, -87.6500")
	}
}
