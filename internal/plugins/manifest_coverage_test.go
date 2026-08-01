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

package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchManifestSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/manifest" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"bill-tracker","name":"Bill Tracker","version":"1.0.0","views":[{"id":"bills","enabled":true,"label":"Bills","icon":"<svg></svg>"}],"provides_events":true}`))
	}))
	t.Cleanup(srv.Close)

	m, err := FetchManifest(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	if m.ID != "bill-tracker" || len(m.Views) != 1 || !m.Views[0].Enabled || m.Views[0].Label != "Bills" || !m.ProvidesEvents {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}

func TestFetchManifestRequestCreationError(t *testing.T) {
	_, err := FetchManifest(context.Background(), ":not-a-url", "tok")
	if err == nil {
		t.Fatal("expected an error constructing the request")
	}
}

func TestFetchManifestDoError(t *testing.T) {
	_, err := FetchManifest(context.Background(), "http://127.0.0.1:1", "tok")
	if err == nil || !strings.Contains(err.Error(), "fetching manifest") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchManifestNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	_, err := FetchManifest(context.Background(), srv.URL, "tok")
	if err == nil || !strings.Contains(err.Error(), "unexpected status 404") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchManifestDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	t.Cleanup(srv.Close)

	_, err := FetchManifest(context.Background(), srv.URL, "tok")
	if err == nil || !strings.Contains(err.Error(), "decoding manifest") {
		t.Fatalf("err = %v", err)
	}
}
