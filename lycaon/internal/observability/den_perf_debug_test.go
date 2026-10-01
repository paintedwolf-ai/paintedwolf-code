package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDenPerfDebugEnabledByMainSwitch(t *testing.T) {
	t.Setenv("LYCAON_DEN_PERF_DEBUG", "")
	t.Setenv("LYCAON_DEBUG_ALL", "")
	if DenPerfDebugEnabled() {
		t.Fatal("expected den perf capture off by default")
	}
	t.Setenv("LYCAON_DEBUG_ALL", "1")
	if !DenPerfDebugEnabled() {
		t.Fatal("expected den perf capture enabled by LYCAON_DEBUG_ALL")
	}
}

func TestLogDenPerfEventWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "den-perf.jsonl")
	t.Setenv("LYCAON_DEN_PERF_DEBUG", "1")
	t.Setenv("LYCAON_DEN_PERF_DEBUG_FILE", logPath)
	CloseDenPerfDebug()

	LogDenPerfEvent(DenPerfDebugEntry{
		Time:  time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		Event: "loop-stall",
		Detail: map[string]any{
			"lag_ms": 340,
			"recent": "thumbnail.capture:start@-40",
		},
	})

	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read capture", err)
	var entry DenPerfDebugEntry
	testutil.FailErr(t, "unmarshal", json.Unmarshal(data[:len(data)-1], &entry))
	if entry.Event != "loop-stall" || entry.Channel != "perf" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Detail["lag_ms"] != float64(340) {
		t.Fatalf("detail = %+v", entry.Detail)
	}
}
