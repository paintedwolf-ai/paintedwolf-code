package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestReorderToolBatchExecutionPutsTerminalToolsLast(t *testing.T) {
	calls := []api.ToolCall{
		{Name: "wait", Args: map[string]any{"timeout_ms": 60_000}},
		{Name: "read", Args: map[string]any{"path": "x.go"}},
	}
	got := reorderToolBatchExecution(batchTestRegistry(t, "wait", "read"), calls)
	if len(got) != 2 || got[0].Name != "read" || got[1].Name != "wait" {
		t.Fatalf("got = %+v", got)
	}
}
