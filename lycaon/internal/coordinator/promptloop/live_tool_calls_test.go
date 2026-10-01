package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestLiveProjectedToolCallsKeepsNamedLongRunningOnly(t *testing.T) {
	got := liveProjectedToolCalls([]api.ToolCall{
		{ID: "c1", Name: "command", Args: map[string]any{"command": "./task check-fast"}},
		{ID: "r1", Name: "read", Args: map[string]any{"path": "a.go"}},
		{ID: "", Name: "verify"},
		{Name: "summarize", Args: map[string]any{"path": "pkg"}},
		{ID: "v1", Name: "verify"},
	})
	if len(got) != 2 {
		t.Fatalf("liveProjectedToolCalls = %+v want command + verify", got)
	}
	if got[0].ID != "c1" || got[0].Name != "command" {
		t.Fatalf("first = %+v want command c1", got[0])
	}
	if got[1].ID != "v1" || got[1].Name != "verify" {
		t.Fatalf("second = %+v want verify v1", got[1])
	}
}

// A worker dispatch settles on ui_visibility: a refused batch comes back benign.
func TestLiveProjectedToolCallsWithholdsWorkerDispatch(t *testing.T) {
	got := liveProjectedToolCalls([]api.ToolCall{
		{ID: "t1", Name: "task", Args: map[string]any{"agent_type": "path-explorer"}},
		{ID: "d1", Name: "delegate_dispatch", Args: map[string]any{"brief": "leg"}},
	})
	if got != nil {
		t.Fatalf("liveProjectedToolCalls = %+v want no worker-dispatch projection", got)
	}
}

func TestLiveProjectedToolCallsEmpty(t *testing.T) {
	if liveProjectedToolCalls(nil) != nil {
		t.Fatal("nil calls should project nothing")
	}
	if liveProjectedToolCalls([]api.ToolCall{{ID: "r1", Name: "read"}}) != nil {
		t.Fatal("deferred tools must not project")
	}
}
