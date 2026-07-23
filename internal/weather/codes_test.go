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

package weather

import "testing"

func TestDescribeCodeKnownCodes(t *testing.T) {
	for code, want := range wmoCodes {
		got := DescribeCode(code)
		if got.Icon == "" || got.Description == "" {
			t.Errorf("code %d: expected non-empty icon/description, got %+v", code, got)
		}
		if got != want {
			t.Errorf("code %d: got %+v, want %+v", code, got, want)
		}
	}
}

func TestDescribeCodeUnknownFallsBackToCloud(t *testing.T) {
	got := DescribeCode(999)
	if got.Icon != "cloud" {
		t.Errorf("unknown code: got icon %q, want fallback \"cloud\"", got.Icon)
	}
}
