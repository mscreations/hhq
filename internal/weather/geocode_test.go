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
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGeocodeParsesFirstResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("name"); got != "Chicago" {
			t.Errorf("expected name query param 'Chicago', got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"name":"Chicago","admin1":"Illinois","country":"United States","latitude":41.85,"longitude":-87.65}]}`))
	}))
	defer srv.Close()

	orig := GeocodeURL
	GeocodeURL = srv.URL
	defer func() { GeocodeURL = orig }()

	loc, err := Geocode(context.Background(), "Chicago")
	if err != nil {
		t.Fatalf("Geocode: %v", err)
	}
	if loc.Name != "Chicago, Illinois, United States" {
		t.Errorf("got name %q", loc.Name)
	}
	if loc.Lat != 41.85 || loc.Lon != -87.65 {
		t.Errorf("got lat=%v lon=%v", loc.Lat, loc.Lon)
	}
}

func TestGeocodeNoResultsReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	orig := GeocodeURL
	GeocodeURL = srv.URL
	defer func() { GeocodeURL = orig }()

	if _, err := Geocode(context.Background(), "Nowhereville"); err == nil {
		t.Fatal("expected an error when no results are found")
	}
}
