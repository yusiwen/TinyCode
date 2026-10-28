// Package logging is the service's tiny leveled logger.
package logging

import (
	"fmt"
	"io"
	"strings"
)

// Level is how much a logger writes.
type Level int

// Levels, from quietest to loudest.
const (
	LevelError Level = iota
	LevelWarn
	LevelInfo
	LevelDebug
)

// Logger writes one line per record, prefixed with its level.
type Logger struct {
	Out   io.Writer
	Level Level
}

// Printf writes one record when level is at or below the logger's own.
func (l *Logger) Printf(level Level, format string, args ...any) {
	if l.Out == nil || level > l.Level {
		return
	}
	fmt.Fprintf(l.Out, "%s: %s\n", strings.ToUpper(level.String()), fmt.Sprintf(format, args...))
}

// String names the level.
func (l Level) String() string {
	switch l {
	case LevelError:
		return "error"
	case LevelWarn:
		return "warn"
	case LevelInfo:
		return "info"
	}
	return "debug"
}
