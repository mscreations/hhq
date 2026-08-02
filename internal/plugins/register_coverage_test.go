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
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterSuccess(t *testing.T) {
	var gotSecret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/register" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		gotSecret = r.Header.Get(connectionSecretHeader)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"token":"secret-token"}`))
	}))
	t.Cleanup(srv.Close)

	token, err := Register(context.Background(), srv.URL, "test-secret")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if token != "secret-token" {
		t.Fatalf("token = %q", token)
	}
	if gotSecret != "test-secret" {
		t.Fatalf("connection secret header = %q, want %q", gotSecret, "test-secret")
	}
}

func TestRegisterRequestCreationError(t *testing.T) {
	_, err := Register(context.Background(), ":not-a-url", "test-secret")
	if err == nil {
		t.Fatal("expected an error constructing the request")
	}
}

func TestRegisterDoError(t *testing.T) {
	_, err := Register(context.Background(), "http://127.0.0.1:1", "test-secret")
	if err == nil || !strings.Contains(err.Error(), "registering") {
		t.Fatalf("err = %v", err)
	}
}

// TestRegisterUnauthorizedStatusIsConnectionSecretMismatch is a direct
// regression test for the 401-vs-403 distinction (see this file's doc
// comment and PLUGINS.md): a plugin rejecting the connection secret with
// 401 must be recognized as ErrConnectionSecretMismatch specifically, with
// an actionable message, not treated as a generic/transient failure.
func TestRegisterUnauthorizedStatusIsConnectionSecretMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	_, err := Register(context.Background(), srv.URL, "wrong-secret")
	if !errors.Is(err, ErrConnectionSecretMismatch) {
		t.Fatalf("err = %v, want errors.Is(err, ErrConnectionSecretMismatch)", err)
	}
	if !strings.Contains(err.Error(), "PLUGIN_CONNECTION_SECRET") {
		t.Fatalf("err = %v, want an actionable message mentioning PLUGIN_CONNECTION_SECRET", err)
	}
}

func TestRegisterNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	_, err := Register(context.Background(), srv.URL, "test-secret")
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

	_, err := Register(context.Background(), srv.URL, "test-secret")
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

	_, err := Register(context.Background(), srv.URL, "test-secret")
	if err == nil || !strings.Contains(err.Error(), "empty token") {
		t.Fatalf("err = %v", err)
	}
}
