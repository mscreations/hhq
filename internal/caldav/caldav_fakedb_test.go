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

package caldav

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
)

// --- a minimal fake database/sql driver ---
//
// A few error branches in discoverAndUpsert/syncCalendarEvents are
// impractical to trigger against a real Postgres instance, because they
// require one specific statement in a sequence to fail while the
// statements around it succeed (e.g. "the color-picking SELECT fails, but
// the very next INSERT must still succeed"). This fake driver scripts
// individual queries/execs to fail or succeed independently, by matching a
// substring of the SQL text, while the code under test and a real
// (httptest) CalDAV server are otherwise exercised exactly as in
// caldav_test.go/caldav_more_test.go.

type fakeQueryFunc func(query string, args []driver.NamedValue) (driver.Rows, error)
type fakeExecFunc func(query string, args []driver.NamedValue) (driver.Result, error)

type fakeDBConfig struct {
	query fakeQueryFunc
	exec  fakeExecFunc
}

var fakeDriverConfigs = struct {
	mu   sync.Mutex
	byID map[string]*fakeDBConfig
}{byID: map[string]*fakeDBConfig{}}

type fakeSQLDriver struct{}

func (fakeSQLDriver) Open(name string) (driver.Conn, error) {
	fakeDriverConfigs.mu.Lock()
	cfg := fakeDriverConfigs.byID[name]
	fakeDriverConfigs.mu.Unlock()
	if cfg == nil {
		return nil, fmt.Errorf("fakeSQLDriver: no config registered for %q", name)
	}
	return &fakeConn{cfg: cfg}, nil
}

func init() {
	sql.Register("caldav-fake", fakeSQLDriver{})
}

// newFakeDB registers query/exec handlers under a name unique to the
// running test and returns a *sql.DB backed by them.
func newFakeDB(t *testing.T, query fakeQueryFunc, exec fakeExecFunc) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano())

	fakeDriverConfigs.mu.Lock()
	fakeDriverConfigs.byID[name] = &fakeDBConfig{query: query, exec: exec}
	fakeDriverConfigs.mu.Unlock()
	t.Cleanup(func() {
		fakeDriverConfigs.mu.Lock()
		delete(fakeDriverConfigs.byID, name)
		fakeDriverConfigs.mu.Unlock()
	})

	db, err := sql.Open("caldav-fake", name)
	if err != nil {
		t.Fatalf("sql.Open(caldav-fake): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type fakeConn struct {
	cfg *fakeDBConfig
}

func (c *fakeConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("fakeConn: Prepare not supported, expected QueryerContext/ExecerContext to be used instead")
}
func (c *fakeConn) Close() error { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fakeConn: transactions not supported")
}

// CheckNamedValue accepts every argument as-is (no type conversion) -
// database/sql's default converter rejects Go types like []string that
// pgx's real driver natively supports, so the fake driver opts out of that
// conversion entirely since these tests never need to inspect args.
func (c *fakeConn) CheckNamedValue(nv *driver.NamedValue) error { return nil }

func (c *fakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.cfg.query == nil {
		return nil, fmt.Errorf("fakeConn: no query handler configured, got query: %s", query)
	}
	return c.cfg.query(query, args)
}

func (c *fakeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.cfg.exec == nil {
		return nil, fmt.Errorf("fakeConn: no exec handler configured, got query: %s", query)
	}
	return c.cfg.exec(query, args)
}

var (
	_ driver.QueryerContext    = (*fakeConn)(nil)
	_ driver.ExecerContext     = (*fakeConn)(nil)
	_ driver.NamedValueChecker = (*fakeConn)(nil)
)

type fakeRows struct {
	cols []string
	data [][]driver.Value
	pos  int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.pos])
	r.pos++
	return nil
}

type fakeResult struct{ rowsAffected int64 }

func (r fakeResult) LastInsertId() (int64, error) { return 0, errors.New("fakeResult: not supported") }
func (r fakeResult) RowsAffected() (int64, error) { return r.rowsAffected, nil }

// --- tests using the fake driver ---

// TestDiscoverAndUpsertColorFallbackWhenNextAvailableColorFails covers
// discoverAndUpsert's color-assignment-failure branch: NextAvailableColor
// erroring must not block the calendar from being usable - it falls back
// to a hardcoded default color and keeps going.
func TestDiscoverAndUpsertColorFallbackWhenNextAvailableColorFails(t *testing.T) {
	const principal = "/principals/user/"
	const homeSet = "/calendars/user/"
	const calPath = homeSet + "home/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/":
			testutil.WritePrincipalResponse(w, principal)
		case r.Method == "PROPFIND" && r.URL.Path == principal:
			testutil.WriteHomeSetResponse(w, principal, homeSet)
		case r.Method == "PROPFIND" && r.URL.Path == homeSet:
			testutil.WriteCalendarListResponse(w, homeSet, map[string]string{calPath: "Home"})
		default:
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "user", "password", models.ProviderGeneric)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	db := newFakeDB(t,
		func(query string, args []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(query, "SELECT color FROM hhq_calendars"):
				return nil, errors.New("simulated color-query failure")
			case strings.Contains(query, "INSERT INTO hhq_calendars"):
				return &fakeRows{cols: []string{"id"}, data: [][]driver.Value{{int64(1)}}}, nil
			case strings.Contains(query, "WHERE c.id = $1"):
				return &fakeRows{
					cols: []string{"id", "calendar_account_id", "account_name", "provider", "external_path", "name", "color", "enabled", "last_synced_at", "last_sync_error", "created_at"},
					data: [][]driver.Value{{int64(1), int64(42), "Test Account", "caldav_generic", calPath, "Home", "#3B82F6", true, nil, nil, time.Now()}},
				}, nil
			default:
				return nil, fmt.Errorf("unexpected query: %s", query)
			}
		},
		nil,
	)

	calendars := &models.CalendarStore{DB: db}
	account := models.CalendarAccount{ID: 42, Name: "Test Account"}

	result, err := discoverAndUpsert(context.Background(), calendars, client, account)
	if err != nil {
		t.Fatalf("discoverAndUpsert: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("got %d calendars, want 1", len(result))
	}
	if result[0].Color != "#3B82F6" {
		t.Fatalf("Color = %q, want the hardcoded fallback used when NextAvailableColor fails", result[0].Color)
	}
}

// TestDiscoverAndUpsertSkipsCalendarWhenGetByIDFails covers
// discoverAndUpsert's reload-after-upsert-failure branch: if GetByID fails
// right after a successful UpsertDiscovered, that calendar must be skipped
// (logged, not returned) rather than aborting the whole discovery pass.
func TestDiscoverAndUpsertSkipsCalendarWhenGetByIDFails(t *testing.T) {
	const principal = "/principals/user/"
	const homeSet = "/calendars/user/"
	const calPath = homeSet + "home/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/":
			testutil.WritePrincipalResponse(w, principal)
		case r.Method == "PROPFIND" && r.URL.Path == principal:
			testutil.WriteHomeSetResponse(w, principal, homeSet)
		case r.Method == "PROPFIND" && r.URL.Path == homeSet:
			testutil.WriteCalendarListResponse(w, homeSet, map[string]string{calPath: "Home"})
		default:
			http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "user", "password", models.ProviderGeneric)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	db := newFakeDB(t,
		func(query string, args []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(query, "SELECT color FROM hhq_calendars"):
				return &fakeRows{cols: []string{"color"}}, nil
			case strings.Contains(query, "INSERT INTO hhq_calendars"):
				return &fakeRows{cols: []string{"id"}, data: [][]driver.Value{{int64(1)}}}, nil
			case strings.Contains(query, "WHERE c.id = $1"):
				return nil, errors.New("simulated reload failure")
			default:
				return nil, fmt.Errorf("unexpected query: %s", query)
			}
		},
		nil,
	)

	calendars := &models.CalendarStore{DB: db}
	account := models.CalendarAccount{ID: 42, Name: "Test Account"}

	result, err := discoverAndUpsert(context.Background(), calendars, client, account)
	if err != nil {
		t.Fatalf("discoverAndUpsert: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected the calendar to be skipped when reloading it after upsert fails, got %+v", result)
	}
}

// TestSyncCalendarEventsRecordsPruneStaleFailure covers syncCalendarEvents'
// events.PruneStale-error branch: the event Upsert must have already
// succeeded (so this is distinct from TestSyncCalendarEventsRecordsUpsertFailure
// in caldav_more_test.go), and the subsequent PruneStale failure must be
// logged/recorded via MarkSynced rather than panicking.
func TestSyncCalendarEventsRecordsPruneStaleFailure(t *testing.T) {
	const calPath = "/calendars/user/home/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "REPORT" && r.URL.Path == calPath {
			writeCalendarQueryResponse(w, calPath+"event-1.ics",
				newTestEventComponent("event-1@example.com", "Fake Event", "",
					time.Now(), time.Now().Add(time.Hour), false))
			return
		}
		http.Error(w, "unexpected request: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}))
	defer srv.Close()

	client, err := NewClient(srv.URL, "user", "password", models.ProviderGeneric)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var upsertCalled, pruneCalled, markSyncedCalled bool

	db := newFakeDB(t, nil, func(query string, args []driver.NamedValue) (driver.Result, error) {
		switch {
		case strings.Contains(query, "INSERT INTO hhq_calendar_events_cache"):
			upsertCalled = true
			return fakeResult{rowsAffected: 1}, nil
		case strings.Contains(query, "DELETE FROM hhq_calendar_events_cache"):
			pruneCalled = true
			return nil, errors.New("simulated prune failure")
		case strings.Contains(query, "UPDATE hhq_calendars SET last_synced_at"):
			markSyncedCalled = true
			return fakeResult{rowsAffected: 1}, nil
		default:
			return nil, fmt.Errorf("unexpected exec: %s", query)
		}
	})

	calendars := &models.CalendarStore{DB: db}
	events := &models.EventStore{DB: db}
	cal := models.Calendar{ID: 1, Name: "Home", ExternalPath: calPath}

	syncCalendarEvents(context.Background(), calendars, events, cal, client,
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	if !upsertCalled {
		t.Error("expected the event Upsert to run before PruneStale")
	}
	if !pruneCalled {
		t.Error("expected PruneStale to be called and fail")
	}
	if !markSyncedCalled {
		t.Error("expected MarkSynced to be called to record PruneStale's failure")
	}
}
