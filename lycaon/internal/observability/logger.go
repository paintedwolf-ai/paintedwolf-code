// Package observability provides structured logging, debug capture, and
// redaction/scrubbing helpers for the sidecar.
package observability

import (
	"log/slog"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/debugpaths"
)

// ParseLogLevel maps LYCAON_LOG_LEVEL (debug, info, warn, error) to slog levels.
// Empty or unknown values default to info.
func ParseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// resolveLogLevel defaults to debug under LYCAON_DEBUG_ALL unless LYCAON_LOG_LEVEL is set.
func resolveLogLevel() slog.Level {
	raw := strings.TrimSpace(os.Getenv("LYCAON_LOG_LEVEL"))
	if raw == "" && debugpaths.FullDebugEnabled() {
		return slog.LevelDebug
	}
	return ParseLogLevel(raw)
}

// NewServeLogger builds the stderr logger used by `lycaon serve` and
// establishes the run's capture session directory. The same redacted records
// reach sidecar.log there under full debug logging, or wherever
// LYCAON_LOG_FILE points.
func NewServeLogger() *slog.Logger {
	closeServeLogFile()

	level := resolveLogLevel()
	stderrHandler := newTextHandler(ConsoleStderr(), level)

	if _, err := debugpaths.EnsureSessionDir(); err != nil {
		slog.New(stderrHandler).Warn("debug capture session dir unavailable", "err", err)
	}
	path := ResolveLogFilePath()
	if path == "" {
		return slog.New(stderrHandler)
	}

	f, err := openServeLogFile(path)
	if err != nil {
		slog.New(stderrHandler).Warn("file logging disabled", "path", path, "err", err)
		return slog.New(stderrHandler)
	}

	serveLogMu.Lock()
	activeLogFile = f
	activeLogFilePath = path
	serveLogMu.Unlock()
	return slog.New(NewMultiHandler(stderrHandler, newTextHandler(f, level)))
}

// Component returns a child logger with a stable component attribute.
func Component(parent *slog.Logger, name string) *slog.Logger {
	if parent == nil {
		parent = slog.Default()
	}
	return parent.With("component", strings.TrimSpace(name))
}
