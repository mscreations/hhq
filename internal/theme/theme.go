// Package theme is the single source of truth for hhq's named color
// themes. Both the parent dashboard (client-side picker) and the kiosk
// (server-persisted setting) select a theme by name; a plugin's proxied
// settings page (internal/plugins/proxy.go) has the resolved variable
// values for a theme injected directly into its HTML, since it can't see
// hhq's own stylesheets. web/static/css/themes.css defines the same
// theme names/values for CSS-native consumers (the dashboard, the
// kiosk, and any inlined plugin view) - keep the two in sync by hand
// when adding or adjusting a theme; there is no shared generator.
package theme

// Vars is the canonical set of CSS custom properties every theme must
// define. The names here (without the "--hhq-" prefix) are exactly what
// PLUGINS.md documents as the plugin theme contract.
type Vars struct {
	Bg      string
	PanelBg string
	Border  string
	Text    string
	TextDim string
	Accent  string
	Green   string
	Red     string
	Amber   string
	Gold    string
}

// Theme is one named, selectable color theme.
type Theme struct {
	Name  string // stable id, used as the data-theme attribute value and settings value
	Label string // shown in the theme picker <select>
	Vars  Vars
}

// DefaultName is used when no theme has been explicitly chosen (the
// kiosk's kiosk_theme setting default, and the parent dashboard's
// pre-any-choice fallback alongside the OS prefers-color-scheme media
// query).
const DefaultName = "dark"

var themes = []Theme{
	{
		Name:  "light",
		Label: "Light",
		Vars: Vars{
			Bg: "#f7f8fa", PanelBg: "#ffffff", Border: "#e2e5e9",
			Text: "#1c1f24", TextDim: "#6b7280", Accent: "#3B82F6",
			Green: "#22c55e", Red: "#ef4444", Amber: "#f59e0b", Gold: "#caa000",
		},
	},
	{
		Name:  "dark",
		Label: "Dark",
		Vars: Vars{
			Bg: "#111418", PanelBg: "#1b1f26", Border: "#2a2f38",
			Text: "#f0f2f5", TextDim: "#9aa3af", Accent: "#5b9bf6",
			Green: "#34d399", Red: "#f87171", Amber: "#fbbf24", Gold: "#ffd700",
		},
	},
	{
		Name:  "midnight",
		Label: "Midnight",
		Vars: Vars{
			Bg: "#0d1526", PanelBg: "#16213a", Border: "#26355a",
			Text: "#e8edf7", TextDim: "#8fa3c4", Accent: "#6c8ff8",
			Green: "#3ddc97", Red: "#ff6b81", Amber: "#ffce54", Gold: "#ffd700",
		},
	},
	{
		Name:  "forest",
		Label: "Forest",
		Vars: Vars{
			Bg: "#0f1f16", PanelBg: "#16291d", Border: "#2c4433",
			Text: "#eaf3ec", TextDim: "#93b39e", Accent: "#5fae7a",
			Green: "#4fbd6f", Red: "#e2665b", Amber: "#d8a657", Gold: "#e8d27a",
		},
	},
	{
		Name:  "sky",
		Label: "Sky Blue",
		Vars: Vars{
			Bg: "#eaf4fb", PanelBg: "#ffffff", Border: "#cfe6f5",
			Text: "#16232b", TextDim: "#55707f", Accent: "#2196c9",
			Green: "#22c55e", Red: "#ef4444", Amber: "#f59e0b", Gold: "#caa000",
		},
	},
	{
		Name:  "lavender",
		Label: "Light Purple",
		Vars: Vars{
			Bg: "#f3effa", PanelBg: "#ffffff", Border: "#e2d6f0",
			Text: "#241b33", TextDim: "#7a6a92", Accent: "#8b5cf6",
			Green: "#22c55e", Red: "#ef4444", Amber: "#f59e0b", Gold: "#b8860b",
		},
	},
	{
		Name:  "peach",
		Label: "Pale Orange",
		Vars: Vars{
			Bg: "#fdf1e7", PanelBg: "#ffffff", Border: "#f3d9bf",
			Text: "#3a2416", TextDim: "#8a6a4f", Accent: "#f97316",
			Green: "#22c55e", Red: "#ef4444", Amber: "#b45309", Gold: "#b8860b",
		},
	},
}

// Available returns every selectable theme, in display order.
func Available() []Theme {
	return themes
}

// ByName returns the theme with the given name, or the default theme if
// name is empty or unrecognized.
func ByName(name string) Theme {
	for _, t := range themes {
		if t.Name == name {
			return t
		}
	}
	return ByName(DefaultName)
}

// IsValidName reports whether name is a known theme.
func IsValidName(name string) bool {
	for _, t := range themes {
		if t.Name == name {
			return true
		}
	}
	return false
}
