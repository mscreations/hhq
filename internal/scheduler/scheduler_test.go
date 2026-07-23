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

package scheduler

import (
	"testing"
	"time"
)

func TestStartOfWeekReturnsSundayMidnight(t *testing.T) {
	tests := []struct {
		name  string
		input time.Time
		want  time.Time
	}{
		{
			name:  "a Wednesday",
			input: time.Date(2026, 8, 5, 14, 30, 0, 0, time.UTC), // Wednesday
			want:  time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),   // preceding Sunday
		},
		{
			name:  "already Sunday",
			input: time.Date(2026, 8, 2, 23, 59, 59, 0, time.UTC),
			want:  time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "a Saturday",
			input: time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := startOfWeek(tt.input)
			if !got.Equal(tt.want) {
				t.Errorf("startOfWeek(%v) = %v, want %v", tt.input, got, tt.want)
			}
			if got.Weekday() != time.Sunday {
				t.Errorf("startOfWeek(%v) weekday = %v, want Sunday", tt.input, got.Weekday())
			}
		})
	}
}
