package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestToolDebugEnabled(t *testing.T) {
	t.Setenv("LYCAON_TOOL_DEBUG", "")
	CloseToolDebug()
	if ToolDebugEnabled() {
		t.Fatal("expected disabled by default")
	}
	t.Setenv("LYCAON_TOOL_DEBUG", "1")
	if !ToolDebugEnabled() {
		t.Fatal("expected enabled for 1")
	}
}

func TestLogToolInvocationWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "tools.jsonl")
	t.Setenv("LYCAON_TOOL_DEBUG", "1")
	t.Setenv("LYCAON_TOOL_DEBUG_FILE", logPath)
	CloseToolDebug()

	LogToolInvocation(ToolInvocationCapture{
		SessionID: "sess-1",
		Tool:      "grep",
		Profile:   "path-explorer",
		Duration:  12 * time.Millisecond,
		Output:    "matches: 3",
		Succeeded: true,
	})

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var entry toolDebugEntry
	if err := json.Unmarshal(data[:len(data)-1], &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if entry.Tool != "grep" || entry.SessionID != "sess-1" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.DurationMs < 0 {
		t.Fatalf("duration_ms = %d", entry.DurationMs)
	}
	if entry.OutputBytes != len("matches: 3") || !entry.Succeeded {
		t.Fatalf("entry = %+v", entry)
	}
}
