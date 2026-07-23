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
