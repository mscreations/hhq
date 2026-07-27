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

package config

import (
	"testing"
	"time"
)

func TestParseChildrenBootstrap(t *testing.T) {
	entries, err := ParseChildrenBootstrap(`[{"name": "Kid One", "color": "#3B82F6"}, {"name": "Kid Two"}]`)
	if err != nil {
		t.Fatalf("ParseChildrenBootstrap: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Name != "Kid One" || entries[0].Color != "#3B82F6" {
		t.Fatalf("entries[0] = %+v", entries[0])
	}
	if entries[1].Name != "Kid Two" || entries[1].Color != "" {
		t.Fatalf("entries[1] = %+v", entries[1])
	}
}

func TestParseChildrenBootstrapInvalidJSON(t *testing.T) {
	if _, err := ParseChildrenBootstrap(`not json`); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestParseChoresBootstrap(t *testing.T) {
	entries, err := ParseChoresBootstrap(`[{"name": "Dishes", "description": "Load and run"}]`)
	if err != nil {
		t.Fatalf("ParseChoresBootstrap: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "Dishes" || entries[0].Description != "Load and run" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseChoresBootstrapInvalidJSON(t *testing.T) {
	if _, err := ParseChoresBootstrap(`not json`); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestParseChoresBootstrapDescriptionAsArray(t *testing.T) {
	entries, err := ParseChoresBootstrap(`[{"name": "Dishes", "description": ["Load", "Run", "Unload"]}]`)
	if err != nil {
		t.Fatalf("ParseChoresBootstrap: %v", err)
	}
	want := "Load\nRun\nUnload"
	if len(entries) != 1 || entries[0].Description != want {
		t.Fatalf("entries = %+v, want description %q", entries, want)
	}
}

func TestParseChoresBootstrapDescriptionOmitted(t *testing.T) {
	entries, err := ParseChoresBootstrap(`[{"name": "Dishes"}]`)
	if err != nil {
		t.Fatalf("ParseChoresBootstrap: %v", err)
	}
	if len(entries) != 1 || entries[0].Description != "" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseChoresBootstrapDescriptionInvalidType(t *testing.T) {
	if _, err := ParseChoresBootstrap(`[{"name": "Dishes", "description": 5}]`); err == nil {
		t.Fatal("expected an error for a non-string/array description")
	}
}

// TestParseChoresBootstrapNameInvalidType covers ChoreBootstrap.UnmarshalJSON's
// initial json.Unmarshal into the raw name/description struct failing - here,
// a non-string "name" can't be parsed into raw.Name (a string field), which
// is a distinct failure point from the description-specific handling covered
// by TestParseChoresBootstrapDescriptionInvalidType above.
func TestParseChoresBootstrapNameInvalidType(t *testing.T) {
	if _, err := ParseChoresBootstrap(`[{"name": 123, "description": "test"}]`); err == nil {
		t.Fatal("expected an error for a non-string name")
	}
}

func TestParseAssignmentsBootstrap(t *testing.T) {
	entries, err := ParseAssignmentsBootstrap(`[
		{"child": "Kid One", "chore": "Dishes", "points": 5, "days_of_week": ["tue", "fri"]},
		{"child": "Kid Two", "chore": "Trash", "one_off_date": "2026-08-01"}
	]`)
	if err != nil {
		t.Fatalf("ParseAssignmentsBootstrap: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Child != "Kid One" || entries[0].Points != 5 || len(entries[0].DaysOfWeek) != 2 {
		t.Fatalf("entries[0] = %+v", entries[0])
	}
	if entries[1].Child != "Kid Two" || entries[1].OneOffDate != "2026-08-01" {
		t.Fatalf("entries[1] = %+v", entries[1])
	}
}

func TestParseAssignmentsBootstrapMultipleChoresPerChild(t *testing.T) {
	entries, err := ParseAssignmentsBootstrap(`[
		{"child": "Alex", "chore": "Dishes", "points": 5, "days_of_week": ["mon"]},
		{"child": "Alex", "chore": "Trash", "points": 2, "days_of_week": ["tue"]}
	]`)
	if err != nil {
		t.Fatalf("ParseAssignmentsBootstrap: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if e.Child != "Alex" {
			t.Fatalf("entries = %+v, want all entries for Alex", entries)
		}
	}
	if entries[0].Chore != "Dishes" || entries[1].Chore != "Trash" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseAssignmentsBootstrapInvalidJSON(t *testing.T) {
	if _, err := ParseAssignmentsBootstrap(`not json`); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestResolveWeekday(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Weekday
		wantErr bool
	}{
		{"sun", time.Sunday, false},
		{"Sunday", time.Sunday, false},
		{"mon", time.Monday, false},
		{"tue", time.Tuesday, false},
		{"wednesday", time.Wednesday, false},
		{"thu", time.Thursday, false},
		{"fri", time.Friday, false},
		{"SAT", time.Saturday, false},
		{"", 0, true},
		{"someday", 0, true},
	}
	for _, c := range cases {
		got, err := ResolveWeekday(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ResolveWeekday(%q) = %v, nil; want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ResolveWeekday(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ResolveWeekday(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
