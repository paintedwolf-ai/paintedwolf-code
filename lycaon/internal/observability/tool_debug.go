package observability

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/runeclamp"
)

// ToolDebugEnabled reports whether native/MCP tool invocations are mirrored to a debug log file.
func ToolDebugEnabled() bool {
	return debugpaths.Enabled(debugpaths.KindTool)
}

type toolDebugEntry struct {
	Time        time.Time `json:"ts"`
	SessionID   string    `json:"session_id,omitempty"`
	Tool        string    `json:"tool"`
	Profile     string    `json:"profile,omitempty"`
	DurationMs  int64     `json:"duration_ms"`
	OutputBytes int       `json:"output_bytes"`
	Truncated   bool      `json:"truncated,omitempty"`
	Succeeded   bool      `json:"succeeded"`
}

var (
	toolDebugOnce sync.Once
	toolDebug     *jsonlDebugLog
)

func initToolDebugLog() {
	if !ToolDebugEnabled() {
		return
	}
	log, err := openJSONLDebugLog(true, debugpaths.KindTool)
	if err != nil {
		slog.Warn("tool debug logging disabled", "err", err)
		return
	}
	toolDebug = log
	slog.Info("tool debug logging enabled", "path", log.path)
}

func activeToolDebugLog() *jsonlDebugLog {
	toolDebugOnce.Do(initToolDebugLog)
	return toolDebug
}

// ToolInvocationCapture is input for tool timing JSONL when LYCAON_TOOL_DEBUG is enabled.
type ToolInvocationCapture struct {
	SessionID string
	Tool      string
	Profile   string
	Duration  time.Duration
	Output    string
	Succeeded bool
}

// LogToolInvocation records a tool round-trip when LYCAON_TOOL_DEBUG is enabled.
func LogToolInvocation(cap ToolInvocationCapture) {
	log := activeToolDebugLog()
	if log == nil {
		return
	}
	out := cap.Output
	truncated := strings.Contains(out, "truncated") || strings.Contains(out, runeclamp.TruncatedSuffix)
	entry := toolDebugEntry{
		Time:        time.Now().UTC(),
		SessionID:   strings.TrimSpace(cap.SessionID),
		Tool:        strings.TrimSpace(cap.Tool),
		Profile:     strings.TrimSpace(cap.Profile),
		DurationMs:  cap.Duration.Milliseconds(),
		OutputBytes: len(out),
		Truncated:   truncated,
		Succeeded:   cap.Succeeded,
	}
	log.write(entry)
}
