package observability

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/debugretention"
)

var (
	serveLogMu        sync.Mutex
	activeLogFilePath string
	activeLogFile     *debugretention.File
)

// ActiveLogFilePath returns the path opened by NewServeLogger, or empty when file logging is off.
func ActiveLogFilePath() string {
	serveLogMu.Lock()
	defer serveLogMu.Unlock()
	return activeLogFilePath
}

// ResolveLogFilePath returns the sidecar log path: LYCAON_LOG_FILE when set,
// else the run's session dir under full debug logging, else none.
func ResolveLogFilePath() string {
	if path := debugpaths.Override(debugpaths.KindSidecarLog); path != "" {
		return path
	}
	if debugpaths.Enabled(debugpaths.KindSidecarLog) {
		return debugpaths.FilePath(debugpaths.KindSidecarLog)
	}
	return ""
}

func openServeLogFile(path string) (*debugretention.File, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("log file path is empty")
	}
	f, err := debugretention.OpenFile(path, debugretention.DefaultConfig().MaxFileBytes)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return f, nil
}

func newTextHandler(w io.Writer, level slog.Level) slog.Handler {
	return NewRedactingHandler(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

func closeServeLogFile() {
	serveLogMu.Lock()
	defer serveLogMu.Unlock()
	if activeLogFile != nil {
		_ = activeLogFile.Close()
		activeLogFile = nil
	}
	activeLogFilePath = ""
}
