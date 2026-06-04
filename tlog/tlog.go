// Package tlog provides structured logging for TinyCode.
// Log format (matching OpenCode):
//
//	LEVEL  YYYY-MM-DDTHH:mm:ss +ELAPSED  service=name key=val... message
package tlog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Level represents a log level.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var levelNames = map[Level]string{
	LevelDebug: "DEBUG",
	LevelInfo:  "INFO",
	LevelWarn:  "WARN",
	LevelError: "ERROR",
}

// ParseLevel parses a level name string.
func ParseLevel(s string) Level {
	switch strings.ToLower(s) {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// Logger is a singleton structured logger.
type Logger struct {
	mu      sync.Mutex
	file    *os.File
	level   Level
	startAt time.Time
	dir     string
}

var defaultLogger = &Logger{
	level:   LevelInfo,
	startAt: time.Now(),
}

// Init initializes the logger with the given directory and minimum level.
// Call once at startup. Creates the log directory if needed.
func Init(dir string, level Level) {
	defaultLogger.mu.Lock()
	defer defaultLogger.mu.Unlock()
	defaultLogger.dir = dir
	defaultLogger.level = level

	if dir == "" {
		return // no file output
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "tlog: failed to create log dir %s: %v\n", dir, err)
		return
	}

	filename := time.Now().Format("2006-01-02T150405") + ".log"
	path := filepath.Join(dir, filename)
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tlog: failed to create log file %s: %v\n", path, err)
		return
	}
	defaultLogger.file = f
}

// SetLevel changes the minimum log level at runtime.
func SetLevel(level Level) {
	defaultLogger.mu.Lock()
	defer defaultLogger.mu.Unlock()
	defaultLogger.level = level
}

// log writes a structured log entry.
func log(level Level, service string, msg string, keysAndValues ...any) {
	L := defaultLogger
	if level < L.level {
		return
	}

	ts := time.Now().Format("2006-01-02T15:04:05")
	elapsed := time.Since(L.startAt)
	var elapsedPart string
	if elapsed < time.Second {
		elapsedPart = fmt.Sprintf("+%dms", elapsed.Milliseconds())
	} else {
		elapsedPart = fmt.Sprintf("+%dms", elapsed.Milliseconds())
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-5s %s %-7s", levelNames[level], ts, elapsedPart))
	sb.WriteString("  service=" + service)

	// Append key=value pairs
	for i := 0; i < len(keysAndValues); i += 2 {
		key := fmt.Sprintf("%v", keysAndValues[i])
		var val string
		if i+1 < len(keysAndValues) {
			val = fmt.Sprintf("%v", keysAndValues[i+1])
		} else {
			val = "(missing)"
		}
		sb.WriteString(" " + key + "=" + val)
	}

	sb.WriteString(" " + msg)
	line := sb.String()

	// Write to file
	L.mu.Lock()
	if L.file != nil {
		L.file.WriteString(line + "\n")
	}
	L.mu.Unlock()
}

// Flush ensures all log entries are written to disk.
func Flush() {
	defaultLogger.mu.Lock()
	defer defaultLogger.mu.Unlock()
	if defaultLogger.file != nil {
		defaultLogger.file.Sync()
	}
}

// Debug logs at DEBUG level.
func Debug(service string, msg string, keysAndValues ...any) {
	log(LevelDebug, service, msg, keysAndValues...)
}

// Info logs at INFO level.
func Info(service string, msg string, keysAndValues ...any) {
	log(LevelInfo, service, msg, keysAndValues...)
}

// Warn logs at WARN level.
func Warn(service string, msg string, keysAndValues ...any) {
	log(LevelWarn, service, msg, keysAndValues...)
}

// Error logs at ERROR level.
func Error(service string, msg string, keysAndValues ...any) {
	log(LevelError, service, msg, keysAndValues...)
}
