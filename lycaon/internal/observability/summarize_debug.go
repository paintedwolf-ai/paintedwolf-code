package observability

import (
	"log/slog"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
)

// SummarizeDebugEnabled reports whether summarize debug capture is active.
func SummarizeDebugEnabled() bool {
	return debugpaths.Enabled(debugpaths.KindSummarize)
}

// SummarizeDebugCapture records one debug-enabled run.
type SummarizeDebugCapture struct {
	Tool              string
	Work              any
	Diagnostics       any
	Task              string
	Mode              string
	CitedFileOmission bool
	OmittedPaths      []string
	MaxAnchors        int
}

type summarizeDebugEntry struct {
	Tool              string    `json:"tool,omitempty"`
	Work              any       `json:"work,omitempty"`
	Diagnostics       any       `json:"diagnostics,omitempty"`
	Time              time.Time `json:"ts"`
	Task              string    `json:"task,omitempty"`
	Mode              string    `json:"mode,omitempty"`
	CitedFileOmission bool      `json:"cited_file_omission"`
	OmittedPaths      []string  `json:"omitted_paths,omitempty"`
	MaxAnchors        int       `json:"max_anchors,omitempty"`
}

var (
	summarizeDebugMu sync.Mutex
	summarizeDebug   *jsonlDebugLog
)

func activeSummarizeDebugLog() *jsonlDebugLog {
	if !SummarizeDebugEnabled() {
		return nil
	}
	summarizeDebugMu.Lock()
	defer summarizeDebugMu.Unlock()
	if summarizeDebug != nil {
		return summarizeDebug
	}
	log, err := openJSONLDebugLog(true, debugpaths.KindSummarize)
	if err != nil {
		slog.Warn("summarize debug logging disabled", "err", err)
		return nil
	}
	summarizeDebug = log
	slog.Info("summarize debug logging enabled", "path", log.path)
	return summarizeDebug
}

// LogSummarizeCall records one summarize run when debugging is active.
func LogSummarizeCall(cap SummarizeDebugCapture) {
	log := activeSummarizeDebugLog()
	if log == nil {
		return
	}
	log.write(summarizeDebugEntry{
		Time:              time.Now().UTC(),
		Tool:              cap.Tool,
		Work:              cap.Work,
		Diagnostics:       cap.Diagnostics,
		Task:              cap.Task,
		Mode:              cap.Mode,
		CitedFileOmission: cap.CitedFileOmission,
		OmittedPaths:      cap.OmittedPaths,
		MaxAnchors:        cap.MaxAnchors,
	})
}

// CloseSummarizeDebug closes summarize capture and clears its state.
func CloseSummarizeDebug() {
	summarizeDebugMu.Lock()
	summarizeDebug.close()
	summarizeDebug = nil
	summarizeDebugMu.Unlock()
}
