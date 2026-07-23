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
	"time"
)

// brokenBodyServer returns an httptest.Server that hijacks the connection
// and writes a response whose declared Content-Length is larger than the
// bytes actually sent, then closes the connection - the client's
// io.ReadAll/json.Decoder sees a genuine mid-body read error (unexpected
// EOF), not just a clean non-200 status or a well-formed-but-wrong body.
// Used to exercise the "reading response body failed" branches in
// FetchView/ProxySettings that a normal canned-response server can't reach.
func brokenBodyServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: 1000\r\n\r\nshort")
		_ = bufrw.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchViewSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/view" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("Authorization header = %q, want Bearer tok", got)
		}
		w.Write([]byte("<p>hello</p>"))
	}))
	t.Cleanup(srv.Close)

	body, err := FetchView(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatalf("FetchView: %v", err)
	}
	if body != "<p>hello</p>" {
		t.Fatalf("body = %q", body)
	}
}

func TestFetchViewRequestCreationError(t *testing.T) {
	// A malformed URL (no scheme, contains an illegal leading colon) causes
	// http.NewRequestWithContext's internal url.Parse to fail before any
	// network call is attempted.
	_, err := FetchView(context.Background(), ":not-a-url", "tok")
	if err == nil {
		t.Fatal("expected an error constructing the request")
	}
}

func TestFetchViewDoError(t *testing.T) {
	// Port 0 on the loopback address is never listening, so the client's Do
	// call fails at the transport level (connection refused).
	_, err := FetchView(context.Background(), "http://127.0.0.1:1", "tok")
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if !strings.Contains(err.Error(), "fetching view") {
		t.Fatalf("error = %v, want it to be wrapped with 'fetching view'", err)
	}
}

func TestFetchViewNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	_, err := FetchView(context.Background(), srv.URL, "tok")
	if err == nil || !strings.Contains(err.Error(), "unexpected status 404") {
		t.Fatalf("err = %v, want unexpected status 404", err)
	}
}

func TestFetchViewReadBodyError(t *testing.T) {
	srv := brokenBodyServer(t)
	_, err := FetchView(context.Background(), srv.URL, "tok")
	if err == nil || !strings.Contains(err.Error(), "reading view response") {
		t.Fatalf("err = %v, want a 'reading view response' error", err)
	}
}

func TestFetchEventsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("from") == "" || r.URL.Query().Get("to") == "" {
			t.Fatalf("expected from/to query params, got %s", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("Authorization header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"events":[{"uid":"a","summary":"Test"}]}`))
	}))
	t.Cleanup(srv.Close)

	from := time.Now()
	to := from.AddDate(0, 0, 7)
	events, err := FetchEvents(context.Background(), srv.URL, "tok", from, to)
	if err != nil {
		t.Fatalf("FetchEvents: %v", err)
	}
	if len(events) != 1 || events[0].UID != "a" {
		t.Fatalf("events = %+v", events)
	}
}

func TestFetchEventsRequestCreationError(t *testing.T) {
	_, err := FetchEvents(context.Background(), ":not-a-url", "tok", time.Now(), time.Now())
	if err == nil {
		t.Fatal("expected an error constructing the request")
	}
}

func TestFetchEventsDoError(t *testing.T) {
	_, err := FetchEvents(context.Background(), "http://127.0.0.1:1", "tok", time.Now(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "fetching events") {
		t.Fatalf("err = %v, want a 'fetching events' error", err)
	}
}

func TestFetchEventsNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	_, err := FetchEvents(context.Background(), srv.URL, "tok", time.Now(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchEventsDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	t.Cleanup(srv.Close)

	_, err := FetchEvents(context.Background(), srv.URL, "tok", time.Now(), time.Now())
	if err == nil || !strings.Contains(err.Error(), "decoding events response") {
		t.Fatalf("err = %v", err)
	}
}

func TestPostActionSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/actions/mark-paid" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("Authorization = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)

	if err := PostAction(context.Background(), srv.URL, "tok", "mark-paid", "uid-1"); err != nil {
		t.Fatalf("PostAction: %v", err)
	}
}

func TestPostActionRequestCreationError(t *testing.T) {
	err := PostAction(context.Background(), ":not-a-url", "tok", "action", "uid")
	if err == nil {
		t.Fatal("expected an error constructing the request")
	}
}

func TestPostActionDoError(t *testing.T) {
	err := PostAction(context.Background(), "http://127.0.0.1:1", "tok", "action", "uid")
	if err == nil || !strings.Contains(err.Error(), "posting action action") {
		t.Fatalf("err = %v", err)
	}
}

func TestPostActionNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	err := PostAction(context.Background(), srv.URL, "tok", "action", "uid")
	if err == nil || !strings.Contains(err.Error(), "unexpected status 400") {
		t.Fatalf("err = %v", err)
	}
}

func TestHealthzSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	if err := Healthz(context.Background(), srv.URL); err != nil {
		t.Fatalf("Healthz: %v", err)
	}
}

func TestHealthzRequestCreationError(t *testing.T) {
	err := Healthz(context.Background(), ":not-a-url")
	if err == nil {
		t.Fatal("expected an error constructing the request")
	}
}

func TestHealthzDoError(t *testing.T) {
	err := Healthz(context.Background(), "http://127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "health check") {
		t.Fatalf("err = %v", err)
	}
}

func TestHealthzNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	err := Healthz(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "unexpected status 503") {
		t.Fatalf("err = %v", err)
	}
}
