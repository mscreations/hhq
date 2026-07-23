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

package models

import (
	"database/sql"
	"testing"
	"time"
)

func TestWeekdayBit(t *testing.T) {
	if WeekdayBit(time.Sunday) != 1 {
		t.Errorf("WeekdayBit(Sunday) = %d, want 1", WeekdayBit(time.Sunday))
	}
	if WeekdayBit(time.Saturday) != 1<<6 {
		t.Errorf("WeekdayBit(Saturday) = %d, want %d", WeekdayBit(time.Saturday), 1<<6)
	}
}

func TestDaysOfWeekContains(t *testing.T) {
	mask := WeekdayBit(time.Monday) | WeekdayBit(time.Wednesday) | WeekdayBit(time.Friday)

	for d := time.Sunday; d <= time.Saturday; d++ {
		want := d == time.Monday || d == time.Wednesday || d == time.Friday
		if got := DaysOfWeekContains(mask, d); got != want {
			t.Errorf("DaysOfWeekContains(mask, %v) = %v, want %v", d, got, want)
		}
	}
}

func TestChoreDefinitionDaysOfWeekLabel(t *testing.T) {
	tests := []struct {
		name string
		def  ChoreDefinition
		want string
	}{
		{
			name: "one-off has no label",
			def:  ChoreDefinition{DaysOfWeek: sql.NullInt32{}},
			want: "",
		},
		{
			name: "single day",
			def:  ChoreDefinition{DaysOfWeek: sql.NullInt32{Int32: int32(WeekdayBit(time.Sunday)), Valid: true}},
			want: "Sun",
		},
		{
			name: "multiple days in week order regardless of bit-set order",
			def: ChoreDefinition{DaysOfWeek: sql.NullInt32{
				Int32: int32(WeekdayBit(time.Friday) | WeekdayBit(time.Tuesday) | WeekdayBit(time.Sunday)),
				Valid: true,
			}},
			want: "Sun, Tue, Fri",
		},
		{
			name: "every day",
			def: ChoreDefinition{DaysOfWeek: sql.NullInt32{
				Int32: int32(0b1111111),
				Valid: true,
			}},
			want: "Sun, Mon, Tue, Wed, Thu, Fri, Sat",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.def.DaysOfWeekLabel(); got != tt.want {
				t.Errorf("DaysOfWeekLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNullableString(t *testing.T) {
	if got := nullableString(""); got.Valid {
		t.Errorf("nullableString(\"\") = %+v, want invalid", got)
	}
	got := nullableString("hello")
	if !got.Valid || got.String != "hello" {
		t.Errorf("nullableString(\"hello\") = %+v, want valid \"hello\"", got)
	}
}
