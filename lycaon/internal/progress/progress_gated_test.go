package progress

import (
	"path/filepath"
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
	if got := FirstProgressGatedTool([]string{"read", "delegate_dispatch"}); got != "delegate_dispatch" {
		t.Fatalf("got %q want delegate_dispatch", got)
	}
	if got := FirstProgressGatedTool([]string{"read", "web_search"}); got != "task" {
		t.Fatalf("got %q want task default when no progress-gated tool", got)
	}
	if got := FirstProgressGatedTool([]string{"read"}); got != "task" {
		t.Fatalf("default = %q want task", got)
	}
}

func TestProgressGatedToolSinceBoundary(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "research libraries"},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "task", Outcome: api.ToolResultOutcomeCompleted}},
	}
	if !ProgressGatedToolSinceBoundary(history, 0) {
		t.Fatal("expected progress-gated tool since boundary")
	}
	if ProgressGatedToolSinceBoundary(history, len(history)) {
		t.Fatal("sinceIdx at end must not match prior call")
	}
}

func TestProgressGatedToolSinceBoundary_ignoresRejectedTask(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "task", Outcome: api.ToolResultOutcomeRejected}},
	}
	if ProgressGatedToolSinceBoundary(history, 0) {
		t.Fatal("rejected task must not count as completed")
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
	root := filepath.Clean(filepath.Join("..", ".."))
	if err := InitProgressGatedTools(root); err != nil {
		t.Fatalf("InitProgressGatedTools: %v", err)
	}
	if !IsProgressGatedTool("task") {
		t.Fatal("catalog must include task")
	}
	if IsProgressGatedTool("web_search") {
		t.Fatal("catalog must not include web_search")
	}
}
