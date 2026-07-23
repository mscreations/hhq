package weather

import (
	"testing"
	"time"

	"github.com/mscreations/hhq/internal/models"
	"github.com/mscreations/hhq/internal/testutil"
)

func TestCacheGetSetClear(t *testing.T) {
	var c Cache

	if _, ok := c.Get(); ok {
		t.Fatal("expected empty cache to report not-ok")
	}

	f := &Forecast{FetchedAt: time.Now(), CurrentTemp: 72}
	c.Set(f)
	got, ok := c.Get()
	if !ok || got != f {
		t.Fatalf("Get() after Set = %+v, %v", got, ok)
	}

	c.Clear()
	if _, ok := c.Get(); ok {
		t.Fatal("expected cache to be empty after Clear")
	}
}

func TestLoadLocationNotConfigured(t *testing.T) {
	conn := testutil.RequireDB(t)
	settings := &models.SettingsStore{DB: conn}

	_, _, _, ok, err := LoadLocation(t.Context(), settings)
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when no location has been configured")
	}
}

func TestLoadLocationConfigured(t *testing.T) {
	conn := testutil.RequireDB(t)
	settings := &models.SettingsStore{DB: conn}
	ctx := t.Context()

	if err := settings.Set(ctx, SettingLat, "41.85"); err != nil {
		t.Fatalf("Set lat: %v", err)
	}
	if err := settings.Set(ctx, SettingLon, "-87.65"); err != nil {
		t.Fatalf("Set lon: %v", err)
	}
	if err := settings.Set(ctx, SettingUnits, string(UnitsMetric)); err != nil {
		t.Fatalf("Set units: %v", err)
	}

	lat, lon, units, ok, err := LoadLocation(ctx, settings)
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true when a location is configured")
	}
	if lat != 41.85 || lon != -87.65 {
		t.Errorf("got lat=%v lon=%v", lat, lon)
	}
	if units != UnitsMetric {
		t.Errorf("got units=%v, want metric", units)
	}
}
