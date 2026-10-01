package observability

import (
	"log/slog"
	"testing"

	"github.com/lycaon/lycaon/internal/debugpaths"
)

// The main LYCAON_DEBUG_ALL switch must enable every debug capture, so a new
// capture category is covered by full debug logging without touching the
// desktop shell or any per-category list.
func TestFullDebugMainSwitchEnablesAllCaptures(t *testing.T) {
	for _, f := range debugpaths.Files() {
		if f.EnableEnv != "" {
			t.Setenv(f.EnableEnv, "")
		}
	}
	t.Setenv(debugpaths.FullDebugEnv, "")

	if debugpaths.FullDebugEnabled() {
		t.Fatal("expected main disabled by default")
	}
	if LLMDebugEnabled() || HTTPDebugEnabled() || SSEDebugEnabled() ||
		ToolDebugEnabled() || WebSearchDebugEnabled() || SummarizeDebugEnabled() ||
		DenPerfDebugEnabled() || PerformanceEnabled() {
		t.Fatal("expected all captures disabled with no env set")
	}

	t.Setenv(debugpaths.FullDebugEnv, "1")
	if !debugpaths.FullDebugEnabled() {
		t.Fatal("expected main enabled for 1")
	}
	captures := map[string]bool{
		"llm":         LLMDebugEnabled(),
		"http":        HTTPDebugEnabled(),
		"sse":         SSEDebugEnabled(),
		"tool":        ToolDebugEnabled(),
		"websearch":   WebSearchDebugEnabled(),
		"summarize":   SummarizeDebugEnabled(),
		"den-perf":    DenPerfDebugEnabled(),
		"performance": PerformanceEnabled(),
	}
	for name, enabled := range captures {
		if !enabled {
			t.Fatalf("%s capture not enabled by main switch", name)
		}
	}
}

func TestResolveLogLevelMainDefaultsToDebug(t *testing.T) {
	t.Setenv("LYCAON_LOG_LEVEL", "")
	t.Setenv(debugpaths.FullDebugEnv, "1")
	if got := resolveLogLevel(); got != slog.LevelDebug {
		t.Fatalf("resolveLogLevel() = %v want debug", got)
	}

	// An explicit level wins over the main switch.
	t.Setenv("LYCAON_LOG_LEVEL", "error")
	if got := resolveLogLevel(); got != slog.LevelError {
		t.Fatalf("resolveLogLevel() = %v want error", got)
	}

	// Main off + unset level falls back to info.
	t.Setenv("LYCAON_LOG_LEVEL", "")
	t.Setenv(debugpaths.FullDebugEnv, "")
	if got := resolveLogLevel(); got != slog.LevelInfo {
		t.Fatalf("resolveLogLevel() = %v want info", got)
	}
}

// Under full debug logging the sidecar log lands in the run's session
// directory beside the captures; without it, only LYCAON_LOG_FILE names one.
func TestSidecarLogFollowsTheSessionDirUnderFullDebug(t *testing.T) {
	t.Setenv("LYCAON_LOG_FILE", "")
	t.Setenv(debugpaths.SessionDirEnv, t.TempDir())
	t.Setenv(debugpaths.FullDebugEnv, "")
	if got := ResolveLogFilePath(); got != "" {
		t.Fatalf("log file resolved without full debug or an override: %q", got)
	}
	t.Setenv(debugpaths.FullDebugEnv, "1")
	if got := ResolveLogFilePath(); got != debugpaths.FilePath(debugpaths.KindSidecarLog) {
		t.Fatalf("log file = %q, want the session dir path", got)
	}
}
