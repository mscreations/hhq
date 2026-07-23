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
