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
	"encoding/json"
	"fmt"
	"net/http"
)

// GeocodeURL is a var (not const) so tests - in this package or others that
// exercise weather.Geocode indirectly (e.g. the parent settings handler) -
// can point it at a local httptest server instead of the real Open-Meteo API.
var GeocodeURL = "https://geocoding-api.open-meteo.com/v1/search"

// Location is a resolved, human-readable place with coordinates.
type Location struct {
	Name string
	Lat  float64
	Lon  float64
}

type geocodeResponse struct {
	Results []struct {
		Name      string  `json:"name"`
		Admin1    string  `json:"admin1"`
		Country   string  `json:"country"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"results"`
}

// Geocode resolves a free-text place name (e.g. "Chicago" or "Springfield, IL")
// to coordinates via Open-Meteo's geocoding API, taking the first (best)
// match. Ambiguous short queries are the caller's problem to refine with a
// more specific query - this doesn't attempt a disambiguation flow.
func Geocode(ctx context.Context, query string) (*Location, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GeocodeURL, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("name", query)
	q.Set("count", "1")
	req.URL.RawQuery = q.Encode()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weather: requesting geocode: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weather: geocode request returned %s", resp.Status)
	}

	var parsed geocodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("weather: decoding geocode response: %w", err)
	}
	if len(parsed.Results) == 0 {
		return nil, fmt.Errorf("weather: no location found for %q", query)
	}

	r := parsed.Results[0]
	name := r.Name
	if r.Admin1 != "" {
		name += ", " + r.Admin1
	}
	if r.Country != "" {
		name += ", " + r.Country
	}

	return &Location{Name: name, Lat: r.Latitude, Lon: r.Longitude}, nil
}
