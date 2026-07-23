package weather

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// sampleOpenMeteoJSON returns a canned response shaped like a real Open-Meteo
// reply, with hourly times anchored around "now" so FetchForecast's
// before-now filtering keeps a predictable number of points regardless of
// when the test runs.
func sampleOpenMeteoJSON(t *testing.T) []byte {
	t.Helper()
	now := time.Now().Truncate(time.Hour)

	hourlyTimes := make([]string, 0, 15)
	hourlyTemps := make([]float64, 0, 15)
	hourlyPrecip := make([]int, 0, 15)
	hourlyCodes := make([]int, 0, 15)
	for i := -2; i < 13; i++ { // include some past hours to verify they're filtered out
		hourlyTimes = append(hourlyTimes, now.Add(time.Duration(i)*time.Hour).Format("2006-01-02T15:04"))
		hourlyTemps = append(hourlyTemps, 60+float64(i))
		hourlyPrecip = append(hourlyPrecip, 10)
		hourlyCodes = append(hourlyCodes, 61)
	}

	dailyTimes := make([]string, 0, 7)
	dailyCodes := make([]int, 0, 7)
	dailyMax := make([]float64, 0, 7)
	dailyMin := make([]float64, 0, 7)
	dailyPrecip := make([]int, 0, 7)
	for i := 0; i < 7; i++ {
		dailyTimes = append(dailyTimes, now.AddDate(0, 0, i).Format("2006-01-02"))
		dailyCodes = append(dailyCodes, 0)
		dailyMax = append(dailyMax, 75)
		dailyMin = append(dailyMin, 55)
		dailyPrecip = append(dailyPrecip, 5)
	}

	resp := openMeteoResponse{}
	resp.CurrentWeather.Temperature = 68.5
	resp.CurrentWeather.WeatherCode = 2
	resp.Hourly.Time = hourlyTimes
	resp.Hourly.Temperature2m = hourlyTemps
	resp.Hourly.PrecipitationProbability = hourlyPrecip
	resp.Hourly.WeatherCode = hourlyCodes
	resp.Daily.Time = dailyTimes
	resp.Daily.WeatherCode = dailyCodes
	resp.Daily.Temperature2mMax = dailyMax
	resp.Daily.Temperature2mMin = dailyMin
	resp.Daily.PrecipitationProbabilityMax = dailyPrecip

	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal sample response: %v", err)
	}
	return b
}

func TestFetchForecastParsesResponseAndFiltersPastHours(t *testing.T) {
	body := sampleOpenMeteoJSON(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	orig := ForecastURL
	ForecastURL = srv.URL
	defer func() { ForecastURL = orig }()

	f, err := FetchForecast(context.Background(), 41.8, -87.6, UnitsImperial)
	if err != nil {
		t.Fatalf("FetchForecast: %v", err)
	}

	if f.CurrentTemp != 68.5 || f.CurrentCode != 2 {
		t.Errorf("current conditions: got temp=%v code=%v", f.CurrentTemp, f.CurrentCode)
	}
	if len(f.Hourly) == 0 {
		t.Fatal("expected at least one hourly point")
	}
	for _, h := range f.Hourly {
		if h.Time.Before(time.Now().Add(-time.Minute)) {
			t.Errorf("hourly point %v should have been filtered out (in the past)", h.Time)
		}
	}
	if len(f.Daily) != 7 {
		t.Errorf("expected 7 daily points, got %d", len(f.Daily))
	}
}

func TestFetchForecastSendsCorrectUnitParams(t *testing.T) {
	body := sampleOpenMeteoJSON(t)
	var gotQuery url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	orig := ForecastURL
	ForecastURL = srv.URL
	defer func() { ForecastURL = orig }()

	if _, err := FetchForecast(context.Background(), 0, 0, UnitsMetric); err != nil {
		t.Fatalf("FetchForecast: %v", err)
	}
	if got := gotQuery.Get("temperature_unit"); got != "celsius" {
		t.Errorf("metric units: temperature_unit = %q, want celsius", got)
	}
	if got := gotQuery.Get("precipitation_unit"); got != "mm" {
		t.Errorf("metric units: precipitation_unit = %q, want mm", got)
	}

	if _, err := FetchForecast(context.Background(), 0, 0, UnitsImperial); err != nil {
		t.Fatalf("FetchForecast: %v", err)
	}
	if got := gotQuery.Get("temperature_unit"); got != "fahrenheit" {
		t.Errorf("imperial units: temperature_unit = %q, want fahrenheit", got)
	}
	if got := gotQuery.Get("precipitation_unit"); got != "inch" {
		t.Errorf("imperial units: precipitation_unit = %q, want inch", got)
	}
}

func TestFetchForecastNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	orig := ForecastURL
	ForecastURL = srv.URL
	defer func() { ForecastURL = orig }()

	if _, err := FetchForecast(context.Background(), 0, 0, UnitsImperial); err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}
