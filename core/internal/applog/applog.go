// Package applog is the daemon's minimal logger.
//
// Deliberately dependency-free: the daemon only needs one line per record on stderr, collected
// by systemd/journalctl or the user's terminal.
package applog

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Level is a message level; the order is severity.
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
		return "DBG"
	case LevelInfo:
		return "INF"
	case LevelWarn:
		return "WRN"
	default:
		return "ERR"
	}
}

var (
	mu     sync.Mutex
	out    io.Writer = os.Stderr
	prefix           = "dcode"
	minLvl           = LevelInfo
)

func init() {
	// DUXCODE_LOG=debug|info|warn|error
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DUXCODE_LOG"))) {
	case "debug":
		minLvl = LevelDebug
	case "warn", "warning":
		minLvl = LevelWarn
	case "error":
		minLvl = LevelError
	}
}

// SetOutput replaces the destination (tests).
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	out = w
}

// SetLevel overrides the minimum level.
func SetLevel(l Level) {
	mu.Lock()
	defer mu.Unlock()
	minLvl = l
}

func logf(l Level, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	if l < minLvl {
		return
	}
	msg := fmt.Sprintf(format, args...)
	// Seconds are enough: this log is read by humans.
	ts := time.Now().Format("15:04:05")
	// A logger cannot report its own write failure anywhere else — stderr is the last resort it has,
	// and falling back to it is what the daemon does when its log file is unwritable.
	_, _ = fmt.Fprintf(out, "%s %s %s: %s\n", ts, l, prefix, msg)
}

func Debugf(format string, args ...any) { logf(LevelDebug, format, args...) }
func Infof(format string, args ...any)  { logf(LevelInfo, format, args...) }
func Warnf(format string, args ...any)  { logf(LevelWarn, format, args...) }
func Errorf(format string, args ...any) { logf(LevelError, format, args...) }
