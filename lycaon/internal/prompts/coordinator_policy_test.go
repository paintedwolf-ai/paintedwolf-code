package prompts_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

func TestCoordinatorReadLineLimitInPolicyVars(t *testing.T) {
	if readcaps.LineLimit <= 0 {
		t.Fatalf("readcaps.LineLimit = %d", readcaps.LineLimit)
	}
}

func TestLaneSyncToolsFiltersVisible(t *testing.T) {
	got := prompts.LaneSyncTools([]string{"task", "read", "list_dir", "grep", "pack_board", "git_status"})
	want := []string{"git_status", "grep", "list_dir", "pack_board", "read"}
	if len(got) != len(want) {
		t.Fatalf("LaneSyncTools = %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("LaneSyncTools = %v want %v", got, want)
		}
	}
}

func TestCoordinatorPolicyGuidanceTracksOfferedTools(t *testing.T) {
	sticky := make([]string, 1, 4)
	sticky[0] = "recall"
	deferred := []string{"command", "capture_page", "summarize"}
	vars := prompts.CoordinatorPolicyTemplateVars(sticky, deferred)
	if vars["profile_has_recall"] != true || vars["more_tools_loadable"] != true {
		t.Fatalf("offered tool guidance: %#v", vars)
	}
	// A loadable tool is untaught until it loads; its schema is not on the call.
	for _, key := range []string{"profile_has_command", "profile_has_capture_page", "profile_has_summarize", "profile_has_verify", "profile_has_page_open", "profile_has_measure_page"} {
		if vars[key] != false {
			t.Fatalf("unoffered tool guidance %s = %v", key, vars[key])
		}
	}
	for _, name := range sticky[:cap(sticky)][1:] {
		if name != "" {
			t.Fatal("guidance construction mutated the sticky schema roster")
		}
	}
}

func TestLaneSyncToolsIncludesListDir(t *testing.T) {
	got := prompts.LaneSyncTools([]string{"list_dir", "find", "grep", "read"})
	want := []string{"find", "grep", "list_dir", "read"}
	if len(got) != len(want) {
		t.Fatalf("LaneSyncTools = %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("LaneSyncTools = %v want %v", got, want)
		}
	}
}

func TestCoordinatorPolicyTemplateVarsKeys(t *testing.T) {
	vars := prompts.CoordinatorPolicyTemplateVars([]string{"read", "grep", "find", "task"}, nil)
	for _, key := range []string{
		"coordinator_read_line_limit",
		"is_coordinator",
		"lane_s_sync_tools",
		"max_in_flight",
		"max_read_workers",
		"max_write_workers",
		"max_concurrent_tool_calls",
		"worker_summary_max_chars",
		"worker_tool_budget_default",
		"hunt_wave_workers",
		"throwaway_tool_loops_min",
		"throwaway_tool_loops_max",
		"deep_tool_loops_hint",
		"max_author_progress_lines",
		"max_progress_label_chars",
	} {
		if _, ok := vars[key]; !ok {
			t.Fatalf("missing policy var %q", key)
		}
	}
	if vars["coordinator_read_line_limit"] != readcaps.LineLimit {
		t.Fatalf("read line limit mismatch")
	}
	if vars["max_concurrent_tool_calls"] != spawn.MaxConcurrentToolCalls {
		t.Fatalf("max_concurrent_tool_calls mismatch")
	}
	if vars["worker_summary_max_chars"] != limits.DefaultWorkerSummaryMaxChars {
		t.Fatalf("worker_summary_max_chars = %v want %d", vars["worker_summary_max_chars"], limits.DefaultWorkerSummaryMaxChars)
	}
	if vars["worker_tool_budget_default"] != spawn.DefaultWorkerMaxToolLoops {
		t.Fatalf("worker default = %v want %d", vars["worker_tool_budget_default"], spawn.DefaultWorkerMaxToolLoops)
	}
	if vars["max_author_progress_lines"] != progress.MaxAuthorProgressLines {
		t.Fatalf("max_author_progress_lines = %v want %d", vars["max_author_progress_lines"], progress.MaxAuthorProgressLines)
	}
	if vars["max_progress_label_chars"] != progress.MaxLabelRunes {
		t.Fatalf("max_progress_label_chars = %v want %d", vars["max_progress_label_chars"], progress.MaxLabelRunes)
	}
}
