// Package logging provides a minimal leveled logger on top of the standard
// library's log package. Level is controlled by the LOG_LEVEL environment
// variable (debug, info, warn, error - default info). Kept intentionally
// simple (no external logging library) since this project doubles as a Go
// learning exercise and the app's logging needs are modest.
package logging

import (
	"log"
	"os"
	"strings"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var current = LevelInfo

func init() {
	current = levelFromEnv(os.Getenv("LOG_LEVEL"))
}

func levelFromEnv(v string) Level {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// Enabled reports whether debug-level logging is on, so call sites can guard
// expensive-to-build debug messages (e.g. dumping full event payloads)
// without paying the formatting cost when debug logging is off.
func DebugEnabled() bool {
	return current <= LevelDebug
}

func Debugf(format string, args ...any) {
	if current <= LevelDebug {
		log.Printf("[DEBUG] "+format, args...)
	}
}

func Infof(format string, args ...any) {
	if current <= LevelInfo {
		log.Printf("[INFO] "+format, args...)
	}
}

func Warnf(format string, args ...any) {
	if current <= LevelWarn {
		log.Printf("[WARN] "+format, args...)
	}
}

// Errorf always logs, regardless of LOG_LEVEL - errors are never suppressed.
func Errorf(format string, args ...any) {
	log.Printf("[ERROR] "+format, args...)
}
