package models

import "testing"

func TestColorName(t *testing.T) {
	tests := []struct {
		hex  string
		want string
	}{
		{"#3B82F6", "Blue"},
		{"#EF4444", "Red"},
		{"#8B5CF6", "Violet"},
		{"#000000", "#000000"}, // unrecognized falls back to the hex itself
		{"", ""},
	}
	for _, tt := range tests {
		if got := ColorName(tt.hex); got != tt.want {
			t.Errorf("ColorName(%q) = %q, want %q", tt.hex, got, tt.want)
		}
	}
}
