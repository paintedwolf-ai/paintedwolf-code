package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

func TestCoordinatorReadLineLimitInPolicyVars(t *testing.T) {
	if readcaps.LineLimit <= 0 {
		t.Fatalf("readcaps.LineLimit = %d", readcaps.LineLimit)
	}
}

func TestImplementDeferUntilUserReplyToolsExcludesTask(t *testing.T) {
	for _, tool := range prompts.ImplementDeferUntilUserReplyTools() {
		if tool == "task" {
			t.Fatal("task must not be in ImplementDeferUntilUserReplyTools")
		}
	}
	if prompts.IsImplementDeferUntilUserReplyTool("task") {
		t.Fatal("task should not defer until user reply")
	}
	for _, tool := range prompts.ImplementDeferUntilUserReplyTools() {
		if !prompts.IsImplementDeferUntilUserReplyTool(tool) {
			t.Fatalf("%q should defer until user reply", tool)
		}
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

func TestCoordinatorWorkerChainPolicyPartialRendersDeferTools(t *testing.T) {
	vars := map[string]any{}
	if err := prompts.MergeCoordinatorKickPolicyVars(vars); err != nil {
		t.Fatalf("MergeCoordinatorKickPolicyVars: %v", err)
	}
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.Render(context.Background(), "partials/coordinator-worker-chain-baseline.md", vars)
	testutil.FailErr(t, "render policy partial", err)
	for _, tool := range prompts.ImplementDeferUntilUserReplyTools() {
		if !strings.Contains(out, tool) {
			t.Fatalf("rendered policy missing defer tool %q:\n%s", tool, out)
		}
	}
}

func TestCoordinatorPolicyTemplateVarsHasListDirWhenVisible(t *testing.T) {
	vars := prompts.CoordinatorPolicyTemplateVars([]string{"list_dir", "find", "grep", "read"}, nil)
	if !vars["profile_has_list_dir"].(bool) {
		t.Fatalf("profile_has_list_dir = %v want true", vars["profile_has_list_dir"])
	}
	if !vars["profile_has_grep"].(bool) {
		t.Fatalf("profile_has_grep = %v want true", vars["profile_has_grep"])
	}
	if !vars["has_find_tool"].(bool) {
		t.Fatalf("has_find_tool = %v want true", vars["has_find_tool"])
	}
	vars = prompts.CoordinatorPolicyTemplateVars([]string{"find", "grep", "read"}, nil)
	if vars["profile_has_list_dir"].(bool) {
		t.Fatalf("profile_has_list_dir = %v want false without list_dir on surface", vars["profile_has_list_dir"])
	}
}

func TestCoordinatorPolicyGuidanceTracksOfferedTools(t *testing.T) {
	sticky := make([]string, 1, 4)
	sticky[0] = "read"
	deferred := []string{"command", "capture_page", "summarize"}
	vars := prompts.CoordinatorPolicyTemplateVars(sticky, deferred)
	if vars["profile_has_read"] != true || vars["more_tools_loadable"] != true {
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

func TestCoordinatorPolicyTemplateVars_hasGitCommitWhenVisible(t *testing.T) {
	vars := prompts.CoordinatorPolicyTemplateVars([]string{"git_commit", "read", "grep"}, nil)
	if !vars["profile_has_git_commit"].(bool) {
		t.Fatalf("profile_has_git_commit = %v want true", vars["profile_has_git_commit"])
	}
	vars = prompts.CoordinatorPolicyTemplateVars([]string{"read", "grep", "task"}, nil)
	if vars["profile_has_git_commit"].(bool) {
		t.Fatalf("profile_has_git_commit = %v want false without git_commit on surface", vars["profile_has_git_commit"])
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
		"defer_until_user_reply_tools",
		"lane_s_sync_tools",
		"max_in_flight",
		"max_read_workers",
		"max_write_workers",
		"max_concurrent_tool_calls",
		"profile_has_git_commit",
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
