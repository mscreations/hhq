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

func TestResolveColor(t *testing.T) {
	cases := []struct {
		value   string
		wantHex string
		wantOK  bool
	}{
		{"red", "#EF4444", true},
		{"Red", "#EF4444", true},
		{"  RED  ", "#EF4444", true},
		{"violet", "#8B5CF6", true},
		{"#3B82F6", "#3B82F6", false}, // a raw hex code isn't a known name
		{"not-a-color", "not-a-color", false},
		{"", "", false},
	}
	for _, c := range cases {
		hex, ok := ResolveColor(c.value)
		if hex != c.wantHex || ok != c.wantOK {
			t.Errorf("ResolveColor(%q) = (%q, %v), want (%q, %v)", c.value, hex, ok, c.wantHex, c.wantOK)
		}
	}
}
