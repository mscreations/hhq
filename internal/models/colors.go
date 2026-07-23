package models

import "strings"

// colorNames maps the hex codes used by colorPalette (calendar.go) and
// childColorPalette (user.go) to human-readable names, so the UI can show
// "Blue" instead of "#3B82F6". Both palettes share the same hex values by
// design (childColorPalette is a deliberate mirror of colorPalette), so one
// map covers both calendars and children.
var colorNames = map[string]string{
	"#3B82F6": "Blue",
	"#EF4444": "Red",
	"#22C55E": "Green",
	"#F59E0B": "Amber",
	"#A855F7": "Purple",
	"#EC4899": "Pink",
	"#14B8A6": "Teal",
	"#F97316": "Orange",
	"#6366F1": "Indigo",
	"#84CC16": "Lime",
	"#06B6D4": "Cyan",
	"#D946EF": "Fuchsia",
	"#EAB308": "Yellow",
	"#10B981": "Emerald",
	"#F43F5E": "Rose",
	"#8B5CF6": "Violet",
	"#0EA5E9": "Sky",
	"#64748B": "Slate",
	"#1E3A8A": "Navy",
	"#991B1B": "Maroon",
	"#166534": "Forest",
	"#CA8A04": "Mustard",
	"#92400E": "Brown",
	"#7C3AED": "Plum",
}

// ColorName returns a human-readable name for a palette hex color code
// (e.g. "#3B82F6" -> "Blue"). Falls back to the hex code itself if it's not
// a recognized palette color (e.g. a value set directly via
// CalendarStore.SetColor rather than auto-assigned from the palette).
func ColorName(hex string) string {
	if name, ok := colorNames[hex]; ok {
		return name
	}
	return hex
}

// nameToHex is the reverse of colorNames, keyed by lowercased name, so a
// palette color can be looked up by its human-readable name (e.g. "red" or
// "Red", matched case-insensitively) as well as by hex code.
var nameToHex = func() map[string]string {
	m := make(map[string]string, len(colorNames))
	for hex, name := range colorNames {
		m[strings.ToLower(name)] = hex
	}
	return m
}()

// ResolveColor resolves a color value to its canonical palette hex code. The
// value may already be a hex code, or a palette color name matched
// case-insensitively against colorNames (e.g. "red"/"Red"/"RED" all resolve
// to "#EF4444"). Used by bootstrap config (see
// internal/handlers/bootstrap_config.go's BootstrapChildren) so
// children.json can specify a color by name instead of requiring a hex code.
// If value doesn't match a known palette name, it's returned unchanged with
// ok false - callers may still accept it as a raw CSS color/hex value.
func ResolveColor(value string) (hex string, ok bool) {
	if hex, found := nameToHex[strings.ToLower(strings.TrimSpace(value))]; found {
		return hex, true
	}
	return value, false
}
