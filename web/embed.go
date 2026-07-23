// Package webassets embeds HappyHome Quest's HTML templates and static assets
// (CSS/JS) directly into the compiled binary. This makes the app independent
// of the current working directory at runtime - previously, template/static
// paths were resolved relative to "web/...", which broke if the binary was
// run from anywhere other than the project root (a common gotcha on Windows
// dev machines and worth avoiding in the container image too).
package webassets

import "embed"

// The "all:" prefix is required here: Go's //go:embed silently EXCLUDES any
// file or directory whose name starts with "_" or "." unless the pattern is
// prefixed with "all:". Without it, the kiosk fragment templates
// (_chores.html, _agenda.html, _calendar.html - named with a leading
// underscore to signal "partial, not a full page") were dropped from the
// embedded filesystem entirely, causing "no such template" at runtime even
// though the files were right there in the source tree.
//
//go:embed all:templates all:static
var FS embed.FS
