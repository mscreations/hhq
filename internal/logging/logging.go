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

// Package logging provides a minimal leveled logger on top of the standard
// library's log package. Level is controlled by the LOG_LEVEL environment
// variable (debug, info, warn, error - default info). Output format is
// controlled by LOG_FORMAT (text, json - default text); json emits one
// JSON object per line (time/level/msg) for log aggregators like Loki that
// parse Kubernetes container logs. Kept intentionally simple (no external
// logging library) since this project doubles as a Go learning exercise and
// the app's logging needs are modest.
package logging

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "info"
	}
}

type OutputFormat int

const (
	FormatText OutputFormat = iota
	FormatJSON
)

var current = LevelInfo
var format = FormatText

func init() {
	current = levelFromEnv(os.Getenv("LOG_LEVEL"))
	format = formatFromEnv(os.Getenv("LOG_FORMAT"))
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

func formatFromEnv(v string) OutputFormat {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "json":
		return FormatJSON
	default:
		return FormatText
	}
}

// Enabled reports whether debug-level logging is on, so call sites can guard
// expensive-to-build debug messages (e.g. dumping full event payloads)
// without paying the formatting cost when debug logging is off.
func DebugEnabled() bool {
	return current <= LevelDebug
}

type jsonEntry struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

func write(level Level, msg string) {
	if format == FormatJSON {
		b, err := json.Marshal(jsonEntry{
			Time:  time.Now().UTC().Format(time.RFC3339Nano),
			Level: level.String(),
			Msg:   msg,
		})
		if err != nil {
			log.Printf("[%s] %s", strings.ToUpper(level.String()), msg)
			return
		}
		log.Writer().Write(append(b, '\n'))
		return
	}
	log.Printf("[%s] %s", strings.ToUpper(level.String()), msg)
}

func Debugf(format string, args ...any) {
	if current <= LevelDebug {
		write(LevelDebug, fmt.Sprintf(format, args...))
	}
}

func Infof(format string, args ...any) {
	if current <= LevelInfo {
		write(LevelInfo, fmt.Sprintf(format, args...))
	}
}

func Warnf(format string, args ...any) {
	if current <= LevelWarn {
		write(LevelWarn, fmt.Sprintf(format, args...))
	}
}

// Errorf always logs, regardless of LOG_LEVEL - errors are never suppressed.
func Errorf(format string, args ...any) {
	write(LevelError, fmt.Sprintf(format, args...))
}
