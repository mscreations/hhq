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

// Package weather fetches current conditions and forecasts from Open-Meteo
// (https://open-meteo.com) - chosen because it's free and keyless, matching
// this app's "no new secrets to manage" approach already used for CalDAV/SMTP.
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Units controls which unit system is requested from Open-Meteo.
type Units string

const (
	UnitsImperial Units = "imperial" // Fahrenheit, mph, inch
	UnitsMetric   Units = "metric"   // Celsius, km/h, mm
)

type HourPoint struct {
	Time              time.Time
	Code              int
	Temp              float64
	PrecipProbability int
}

type DayPoint struct {
	Date              time.Time
	Code              int
	TempMax           float64
	TempMin           float64
	PrecipProbability int
}

// Forecast is the parsed, app-shaped view of an Open-Meteo response.
type Forecast struct {
	FetchedAt   time.Time
	CurrentTemp float64
	CurrentCode int
	Hourly      []HourPoint // next ~12 hours
	Daily       []DayPoint  // next 7 days, including today
}

// ForecastURL is a var (not const) so tests - in this package or others that
// exercise weather.FetchForecast indirectly (e.g. the scheduler) - can point
// it at a local httptest server instead of the real Open-Meteo API.
var ForecastURL = "https://api.open-meteo.com/v1/forecast"

// openMeteoResponse mirrors the subset of Open-Meteo's JSON shape this app
// uses. See https://open-meteo.com/en/docs for the full schema.
type openMeteoResponse struct {
	CurrentWeather struct {
		Temperature float64 `json:"temperature"`
		WeatherCode int     `json:"weathercode"`
	} `json:"current_weather"`
	Hourly struct {
		Time                     []string  `json:"time"`
		Temperature2m            []float64 `json:"temperature_2m"`
		PrecipitationProbability []int     `json:"precipitation_probability"`
		WeatherCode              []int     `json:"weathercode"`
	} `json:"hourly"`
	Daily struct {
		Time                        []string  `json:"time"`
		WeatherCode                 []int     `json:"weathercode"`
		Temperature2mMax            []float64 `json:"temperature_2m_max"`
		Temperature2mMin            []float64 `json:"temperature_2m_min"`
		PrecipitationProbabilityMax []int     `json:"precipitation_probability_max"`
	} `json:"daily"`
}

// FetchForecast calls Open-Meteo for the given coordinates and returns the
// current conditions plus the next ~12 hours and 7 days of forecast data.
func FetchForecast(ctx context.Context, lat, lon float64, units Units) (*Forecast, error) {
	tempUnit := "fahrenheit"
	precipUnit := "inch"
	windUnit := "mph"
	if units == UnitsMetric {
		tempUnit = "celsius"
		precipUnit = "mm"
		windUnit = "kmh"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ForecastURL, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("latitude", fmt.Sprintf("%f", lat))
	q.Set("longitude", fmt.Sprintf("%f", lon))
	q.Set("current_weather", "true")
	q.Set("hourly", "temperature_2m,precipitation_probability,weathercode")
	q.Set("daily", "weathercode,temperature_2m_max,temperature_2m_min,precipitation_probability_max")
	q.Set("temperature_unit", tempUnit)
	q.Set("precipitation_unit", precipUnit)
	q.Set("wind_speed_unit", windUnit)
	q.Set("timeformat", "iso8601")
	q.Set("forecast_days", "7")
	req.URL.RawQuery = q.Encode()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weather: requesting forecast: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weather: forecast request returned %s", resp.Status)
	}

	var parsed openMeteoResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("weather: decoding forecast response: %w", err)
	}

	return buildForecast(&parsed), nil
}

func buildForecast(parsed *openMeteoResponse) *Forecast {
	f := &Forecast{
		FetchedAt:   time.Now(),
		CurrentTemp: parsed.CurrentWeather.Temperature,
		CurrentCode: parsed.CurrentWeather.WeatherCode,
	}

	now := time.Now()
	hourCount := len(parsed.Hourly.Time)
	for i := 0; i < hourCount && len(f.Hourly) < 12; i++ {
		t, err := time.Parse("2006-01-02T15:04", parsed.Hourly.Time[i])
		if err != nil || t.Before(now) {
			continue
		}
		f.Hourly = append(f.Hourly, HourPoint{
			Time:              t,
			Code:              intAt(parsed.Hourly.WeatherCode, i),
			Temp:              valueAt(parsed.Hourly.Temperature2m, i),
			PrecipProbability: intAt(parsed.Hourly.PrecipitationProbability, i),
		})
	}

	dayCount := len(parsed.Daily.Time)
	for i := 0; i < dayCount; i++ {
		d, err := time.Parse("2006-01-02", parsed.Daily.Time[i])
		if err != nil {
			continue
		}
		f.Daily = append(f.Daily, DayPoint{
			Date:              d,
			Code:              intAt(parsed.Daily.WeatherCode, i),
			TempMax:           valueAt(parsed.Daily.Temperature2mMax, i),
			TempMin:           valueAt(parsed.Daily.Temperature2mMin, i),
			PrecipProbability: intAt(parsed.Daily.PrecipitationProbabilityMax, i),
		})
	}

	return f
}

func valueAt(s []float64, i int) float64 {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

func intAt(s []int, i int) int {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}
