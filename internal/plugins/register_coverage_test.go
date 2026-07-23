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

func TestRegisterSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/register" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"token":"secret-token"}`))
	}))
	t.Cleanup(srv.Close)

	token, err := Register(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if token != "secret-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestRegisterRequestCreationError(t *testing.T) {
	_, err := Register(context.Background(), ":not-a-url")
	if err == nil {
		t.Fatal("expected an error constructing the request")
	}
}

func TestRegisterDoError(t *testing.T) {
	_, err := Register(context.Background(), "http://127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "registering") {
		t.Fatalf("err = %v", err)
	}
}

func TestRegisterNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	_, err := Register(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "unexpected status 403") {
		t.Fatalf("err = %v", err)
	}
}

func TestRegisterDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	t.Cleanup(srv.Close)

	_, err := Register(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "decoding register response") {
		t.Fatalf("err = %v", err)
	}
}

func TestRegisterEmptyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"token":""}`))
	}))
	t.Cleanup(srv.Close)

	_, err := Register(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "empty token") {
		t.Fatalf("err = %v", err)
	}
}
