package weather

// This file adds targeted coverage for branches the existing *_test.go files
// (cache_test.go, weather_test.go, geocode_test.go) don't exercise: settings
// store error propagation in LoadLocation/LoadRadarZoom, malformed/edge-case
// Open-Meteo responses in buildForecast/valueAt/intAt, and the
// request-construction / transport / decode error paths in FetchForecast and
// Geocode. New file only - the existing test files are left untouched since
// other work is happening in parallel elsewhere in this checkout.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mscreations/hhq/internal/models"
)

// -----------------------------------------------------------------------
// A minimal fake database/sql driver, used only so LoadLocation/LoadRadarZoom
// can be driven through a settings.Get failure on a *specific* call within a
// single invocation (e.g. the 2nd or 3rd Get of three). A real Postgres
// connection can't be coerced into that deterministically: canceling the
// context fails every subsequent query, not just one, so it can only ever
// exercise the *first* Get's error branch. This fake driver returns a
// canned single-column row (or a deliberate error) per call, counting calls
// across the lifetime of one *sql.DB (pinned to a single connection via
// SetMaxOpenConns(1) so the call order is deterministic).
// -----------------------------------------------------------------------

type fakeSettingsConn struct {
	mu     sync.Mutex
	call   int
	failOn int      // 1-indexed call number to fail on; 0 = never fail
	values []string // per-call return values, 1-indexed (values[call-1])
}

func (c *fakeSettingsConn) next() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.call++
	n := c.call
	if c.failOn != 0 && n == c.failOn {
		return "", errors.New("fakeSettingsConn: simulated query failure")
	}
	if n-1 < len(c.values) {
		return c.values[n-1], nil
	}
	return "", nil
}

func (c *fakeSettingsConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("fakeSettingsConn: Prepare not supported")
}
func (c *fakeSettingsConn) Close() error { return nil }
func (c *fakeSettingsConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fakeSettingsConn: Begin not supported")
}

func (c *fakeSettingsConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	v, err := c.next()
	if err != nil {
		return nil, err
	}
	return &fakeSettingsRows{value: v}, nil
}

type fakeSettingsRows struct {
	value    string
	returned bool
}

func (r *fakeSettingsRows) Columns() []string { return []string{"value"} }
func (r *fakeSettingsRows) Close() error      { return nil }
func (r *fakeSettingsRows) Next(dest []driver.Value) error {
	if r.returned {
		return io.EOF
	}
	dest[0] = r.value
	r.returned = true
	return nil
}

var currentFakeSettingsConn *fakeSettingsConn

type fakeSettingsDriver struct{}

func (fakeSettingsDriver) Open(name string) (driver.Conn, error) {
	return currentFakeSettingsConn, nil
}

func init() {
	sql.Register("weatherfakesettings", fakeSettingsDriver{})
}

// newFakeSettingsStore returns a SettingsStore backed by the fake driver
// above, configured to fail on the failOn'th Get call (0 = never) and to
// return values[i] for the (i+1)th call.
func newFakeSettingsStore(t *testing.T, failOn int, values ...string) *models.SettingsStore {
	t.Helper()
	currentFakeSettingsConn = &fakeSettingsConn{failOn: failOn, values: values}
	db, err := sql.Open("weatherfakesettings", "fake")
	if err != nil {
		t.Fatalf("sql.Open fake driver: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return &models.SettingsStore{DB: db}
}

// -----------------------------------------------------------------------
// LoadRadarZoom
// -----------------------------------------------------------------------

func TestLoadRadarZoomSettingsGetError(t *testing.T) {
	settings := newFakeSettingsStore(t, 1)
	_, err := LoadRadarZoom(context.Background(), settings)
	if err == nil {
		t.Fatal("expected an error when the settings store errors")
	}
}

func TestLoadRadarZoomUnsetReturnsDefault(t *testing.T) {
	settings := newFakeSettingsStore(t, 0, "")
	zoom, err := LoadRadarZoom(context.Background(), settings)
	if err != nil {
		t.Fatalf("LoadRadarZoom: %v", err)
	}
	if zoom != DefaultRadarZoom {
		t.Errorf("got zoom=%d, want default %d", zoom, DefaultRadarZoom)
	}
}

func TestLoadRadarZoomInvalidValueReturnsDefault(t *testing.T) {
	settings := newFakeSettingsStore(t, 0, "not-a-number")
	zoom, err := LoadRadarZoom(context.Background(), settings)
	if err != nil {
		t.Fatalf("LoadRadarZoom: %v", err)
	}
	if zoom != DefaultRadarZoom {
		t.Errorf("got zoom=%d, want default %d for an invalid stored value", zoom, DefaultRadarZoom)
	}
}

func TestLoadRadarZoomValidValue(t *testing.T) {
	settings := newFakeSettingsStore(t, 0, "12")
	zoom, err := LoadRadarZoom(context.Background(), settings)
	if err != nil {
		t.Fatalf("LoadRadarZoom: %v", err)
	}
	if zoom != 12 {
		t.Errorf("got zoom=%d, want 12", zoom)
	}
}

// -----------------------------------------------------------------------
// LoadLocation
// -----------------------------------------------------------------------

func TestLoadLocationLatGetError(t *testing.T) {
	settings := newFakeSettingsStore(t, 1)
	_, _, _, ok, err := LoadLocation(context.Background(), settings)
	if err == nil {
		t.Fatal("expected an error when the lat Get call fails")
	}
	if ok {
		t.Error("expected ok=false on error")
	}
}

func TestLoadLocationLonGetError(t *testing.T) {
	settings := newFakeSettingsStore(t, 2, "41.85")
	_, _, _, ok, err := LoadLocation(context.Background(), settings)
	if err == nil {
		t.Fatal("expected an error when the lon Get call fails")
	}
	if ok {
		t.Error("expected ok=false on error")
	}
}

func TestLoadLocationUnitsGetError(t *testing.T) {
	settings := newFakeSettingsStore(t, 3, "41.85", "-87.65")
	_, _, _, ok, err := LoadLocation(context.Background(), settings)
	if err == nil {
		t.Fatal("expected an error when the units Get call fails")
	}
	if ok {
		t.Error("expected ok=false on error")
	}
}

func TestLoadLocationInvalidLatitude(t *testing.T) {
	// LoadLocation fetches both lat and lon before checking either for
	// emptiness, so lon must be non-empty here too or the empty-lon branch
	// (not this one) short-circuits the function first.
	settings := newFakeSettingsStore(t, 0, "not-a-float", "-87.65")
	_, _, _, ok, err := LoadLocation(context.Background(), settings)
	if err == nil {
		t.Fatal("expected an error for an unparseable latitude")
	}
	if ok {
		t.Error("expected ok=false on parse error")
	}
}

func TestLoadLocationInvalidLongitude(t *testing.T) {
	settings := newFakeSettingsStore(t, 0, "41.85", "not-a-float")
	_, _, _, ok, err := LoadLocation(context.Background(), settings)
	if err == nil {
		t.Fatal("expected an error for an unparseable longitude")
	}
	if ok {
		t.Error("expected ok=false on parse error")
	}
}

// -----------------------------------------------------------------------
// valueAt / intAt - direct unit tests for all three branches each.
// -----------------------------------------------------------------------

func TestValueAtBounds(t *testing.T) {
	s := []float64{1.5, 2.5, 3.5}

	if got := valueAt(s, -1); got != 0 {
		t.Errorf("valueAt(s, -1) = %v, want 0", got)
	}
	if got := valueAt(s, len(s)); got != 0 {
		t.Errorf("valueAt(s, len(s)) = %v, want 0", got)
	}
	if got := valueAt(s, 1); got != 2.5 {
		t.Errorf("valueAt(s, 1) = %v, want 2.5", got)
	}
}

func TestIntAtBounds(t *testing.T) {
	s := []int{10, 20, 30}

	if got := intAt(s, -1); got != 0 {
		t.Errorf("intAt(s, -1) = %v, want 0", got)
	}
	if got := intAt(s, len(s)); got != 0 {
		t.Errorf("intAt(s, len(s)) = %v, want 0", got)
	}
	if got := intAt(s, 1); got != 20 {
		t.Errorf("intAt(s, 1) = %v, want 20", got)
	}
}

// -----------------------------------------------------------------------
// buildForecast - malformed-entry branches (called directly since it's
// unexported but in-package, avoiding the need to round-trip through HTTP).
// -----------------------------------------------------------------------

func TestBuildForecastSkipsUnparseableHourlyTime(t *testing.T) {
	var parsed openMeteoResponse
	parsed.Hourly.Time = []string{"not-a-valid-time", "also-bad"}
	parsed.Hourly.Temperature2m = []float64{1, 2}
	parsed.Hourly.PrecipitationProbability = []int{1, 2}
	parsed.Hourly.WeatherCode = []int{1, 2}

	f := buildForecast(&parsed)
	if len(f.Hourly) != 0 {
		t.Errorf("expected unparseable hourly times to be skipped, got %d points", len(f.Hourly))
	}
}

func TestBuildForecastSkipsUnparseableDailyTime(t *testing.T) {
	var parsed openMeteoResponse
	parsed.Daily.Time = []string{"not-a-valid-date", "2026-07-21"}
	parsed.Daily.WeatherCode = []int{1, 2}
	parsed.Daily.Temperature2mMax = []float64{70, 71}
	parsed.Daily.Temperature2mMin = []float64{50, 51}
	parsed.Daily.PrecipitationProbabilityMax = []int{5, 6}

	f := buildForecast(&parsed)
	if len(f.Daily) != 1 {
		t.Fatalf("expected only the parseable daily entry to survive, got %d", len(f.Daily))
	}
	if f.Daily[0].TempMax != 71 {
		t.Errorf("got TempMax=%v, want the second (parseable) entry's value 71", f.Daily[0].TempMax)
	}
}

// -----------------------------------------------------------------------
// FetchForecast - request-construction / transport / decode error paths.
// -----------------------------------------------------------------------

func TestFetchForecastInvalidURLFailsRequestConstruction(t *testing.T) {
	orig := ForecastURL
	// A control character in the URL makes url.Parse (invoked internally by
	// http.NewRequestWithContext) fail, exercising the request-construction
	// error branch without needing any server at all.
	ForecastURL = "http://example.com/\x00"
	defer func() { ForecastURL = orig }()

	if _, err := FetchForecast(context.Background(), 0, 0, UnitsImperial); err == nil {
		t.Fatal("expected an error when the forecast URL is invalid")
	}
}

func TestFetchForecastTransportErrorOnCanceledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	orig := ForecastURL
	ForecastURL = srv.URL
	defer func() { ForecastURL = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := FetchForecast(ctx, 0, 0, UnitsImperial); err == nil {
		t.Fatal("expected a transport error when the context is already canceled")
	}
}

func TestFetchForecastDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not valid json`))
	}))
	defer srv.Close()

	orig := ForecastURL
	ForecastURL = srv.URL
	defer func() { ForecastURL = orig }()

	if _, err := FetchForecast(context.Background(), 0, 0, UnitsImperial); err == nil {
		t.Fatal("expected an error for a malformed JSON response body")
	}
}

// -----------------------------------------------------------------------
// Geocode - request-construction / transport / decode error paths.
// -----------------------------------------------------------------------

func TestGeocodeInvalidURLFailsRequestConstruction(t *testing.T) {
	orig := GeocodeURL
	GeocodeURL = "http://example.com/\x00"
	defer func() { GeocodeURL = orig }()

	if _, err := Geocode(context.Background(), "Chicago"); err == nil {
		t.Fatal("expected an error when the geocode URL is invalid")
	}
}

func TestGeocodeTransportErrorOnCanceledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	orig := GeocodeURL
	GeocodeURL = srv.URL
	defer func() { GeocodeURL = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Geocode(ctx, "Chicago"); err == nil {
		t.Fatal("expected a transport error when the context is already canceled")
	}
}

func TestGeocodeNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	orig := GeocodeURL
	GeocodeURL = srv.URL
	defer func() { GeocodeURL = orig }()

	if _, err := Geocode(context.Background(), "Chicago"); err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
}

func TestGeocodeDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{not valid json`))
	}))
	defer srv.Close()

	orig := GeocodeURL
	GeocodeURL = srv.URL
	defer func() { GeocodeURL = orig }()

	if _, err := Geocode(context.Background(), "Chicago"); err == nil {
		t.Fatal("expected an error for a malformed JSON response body")
	}
}
