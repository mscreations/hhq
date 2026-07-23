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
