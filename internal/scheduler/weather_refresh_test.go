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

package scheduler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
	"github.com/mscreations/hhq/internal/weather"
)

// fakeWeatherServer serves a minimal valid Open-Meteo-shaped response so
// weather.FetchForecast succeeds without hitting the real API.
func fakeWeatherServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"current_weather":{"temperature":70.5,"weathercode":1},"hourly":{"time":[],"temperature_2m":[],"precipitation_probability":[],"weathercode":[]},"daily":{"time":[],"weathercode":[],"temperature_2m_max":[],"temperature_2m_min":[],"precipitation_probability_max":[]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRefreshWeatherNoLocationConfiguredLeavesCacheEmpty(t *testing.T) {
	conn := testutil.RequireDB(t)

	s := &Scheduler{
		Cfg:      &config.Config{WeatherRefreshInterval: time.Hour},
		Settings: &models.SettingsStore{DB: conn},
		Weather:  &weather.Cache{},
	}

	s.refreshWeather(t.Context())

	if _, ok := s.Weather.Get(); ok {
		t.Fatal("expected cache to remain empty when no location is configured")
	}
}

func TestRefreshWeatherWithLocationPopulatesCache(t *testing.T) {
	conn := testutil.RequireDB(t)
	settings := &models.SettingsStore{DB: conn}
	ctx := t.Context()

	if err := settings.Set(ctx, weather.SettingLat, "41.85"); err != nil {
		t.Fatalf("Set lat: %v", err)
	}
	if err := settings.Set(ctx, weather.SettingLon, "-87.65"); err != nil {
		t.Fatalf("Set lon: %v", err)
	}

	srv := fakeWeatherServer(t)
	origForecastURL := weather.ForecastURL
	weather.ForecastURL = srv.URL
	defer func() { weather.ForecastURL = origForecastURL }()

	s := &Scheduler{
		Cfg:      &config.Config{WeatherRefreshInterval: time.Hour},
		Settings: settings,
		Weather:  &weather.Cache{},
	}

	s.refreshWeather(ctx)

	forecast, ok := s.Weather.Get()
	if !ok {
		t.Fatal("expected cache to be populated after refreshWeather with a configured location")
	}
	if forecast.CurrentTemp != 70.5 || forecast.CurrentCode != 1 {
		t.Errorf("got forecast %+v", forecast)
	}
}
