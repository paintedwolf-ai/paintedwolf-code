package prompts_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

func TestMergeWorkerPolicyTemplateVars(t *testing.T) {
	vars := map[string]any{}
	if err := prompts.MergeWorkerPolicyTemplateVars("path-explorer", vars); err != nil {
		t.Fatalf("MergeWorkerPolicyTemplateVars: %v", err)
	}
	if vars["worker_tool_budget_default"] != spawn.DefaultWorkerMaxToolLoops {
		t.Fatalf("worker default = %v want %d", vars["worker_tool_budget_default"], spawn.DefaultWorkerMaxToolLoops)
	}
	if _, exists := vars["max_tool_loops"]; exists {
		t.Fatal("worker policy vars contain a task-specific max_tool_loops")
	}
	if vars["max_concurrent_tool_calls"] != spawn.MaxConcurrentToolCalls {
		t.Fatalf("max_concurrent_tool_calls mismatch")
	}
	if vars["worker_read_line_limit"] != readcaps.LineLimit {
		t.Fatalf("worker_read_line_limit mismatch")
	}
	if vars["worker_summary_max_chars"] != limits.DefaultWorkerSummaryMaxChars {
		t.Fatalf("worker_summary_max_chars = %v want %d", vars["worker_summary_max_chars"], limits.DefaultWorkerSummaryMaxChars)
	}
	if vars["max_author_progress_lines"] != progress.MaxAuthorProgressLines {
		t.Fatalf("max_author_progress_lines = %v want %d", vars["max_author_progress_lines"], progress.MaxAuthorProgressLines)
	}
	if vars["max_progress_label_chars"] != progress.MaxLabelRunes {
		t.Fatalf("max_progress_label_chars = %v want %d", vars["max_progress_label_chars"], progress.MaxLabelRunes)
	}
}
