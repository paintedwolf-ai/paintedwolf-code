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

func TestSanitizeToolCallsForExecutionPreservesRepeatableStatefulCalls(t *testing.T) {
	calls := []api.ToolCall{
		{ID: "ts1", Name: "terminal_send", Args: map[string]any{"session_id": "term-1", "keys": []any{"Down"}}},
		{ID: "ts2", Name: "terminal_send", Args: map[string]any{"session_id": "term-1", "keys": []any{"Down"}}},
		{ID: "ts2", Name: "terminal_send", Args: map[string]any{"session_id": "term-1", "keys": []any{"Down"}}},
		{ID: "pa1", Name: "page_act", Args: map[string]any{"page_id": "p1", "action": "press", "key": "ArrowDown"}},
		{ID: "pa2", Name: "page_act", Args: map[string]any{"page_id": "p1", "action": "press", "key": "ArrowDown"}},
		{ID: "w1", Name: "write", Args: map[string]any{"path": "log.txt", "content": "line\n", "append": true}},
		{ID: "w2", Name: "write", Args: map[string]any{"path": "log.txt", "content": "line\n", "append": true}},
		{ID: "wo1", Name: "write", Args: map[string]any{"path": "out.txt", "content": "same"}},
		{ID: "wo2", Name: "write", Args: map[string]any{"path": "out.txt", "content": "same"}},
	}
	got := sanitizeToolCallsForExecution(calls)
	if len(got) != 7 {
		t.Fatalf("calls = %d want 7 (distinct stateful calls kept, duplicate ID ts2 and overwrite wo2 dropped): %+v", len(got), got)
	}
}
