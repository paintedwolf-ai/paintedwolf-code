package progress

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestTurnHasProgressGatedTool(t *testing.T) {
	if !TurnHasProgressGatedTool([]string{"read", "task"}) {
		t.Fatal("task should be progress-gated")
	}
	if TurnHasProgressGatedTool([]string{"read", "web_search"}) {
		t.Fatal("web_search is not progress-gated")
	}
	if !TurnHasProgressGatedTool([]string{"read", "write"}) {
		t.Fatal("write should be progress-gated")
	}
	if !IsProgressGatedTool("edit") || !IsProgressGatedTool("replace_lines") || !IsProgressGatedTool("restore_version") {
		t.Fatal("edit, replace_lines, and restore_version should be progress-gated")
	}
}

func TestFirstProgressGatedTool(t *testing.T) {
	if got := FirstProgressGatedTool([]string{"read", "write"}); got != "write" {
		t.Fatalf("got %q want write", got)
	}
	if got := FirstProgressGatedTool([]string{"read", "web_search"}); got != "task" {
		t.Fatalf("got %q want task default when no progress-gated tool", got)
	}
	if got := FirstProgressGatedTool([]string{"read"}); got != "task" {
		t.Fatalf("default = %q want task", got)
	}
}

func TestProgressGatedToolAttemptedSinceBoundary_countsRejectedTask(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "task", Outcome: api.ToolResultOutcomeRejected}},
	}
	if !ProgressGatedToolAttemptedSinceBoundary(history, 0) {
		t.Fatal("rejected progress-gated tool must count as attempted")
	}
}

func TestInitProgressGatedTools_loadsCatalog(t *testing.T) {
	if err := InitProgressGatedTools(); err != nil {
		t.Fatalf("InitProgressGatedTools: %v", err)
	}
	if !IsProgressGatedTool("task") {
		t.Fatal("catalog must include task")
	}
	if IsProgressGatedTool("web_search") {
		t.Fatal("catalog must not include web_search")
	}
	order := GatedToolOrder()
	if len(order) == 0 || order[0] != "restore_version" {
		t.Fatalf("GatedToolOrder = %v, want non-empty starting with restore_version", order)
	}
}
