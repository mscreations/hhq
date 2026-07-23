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
