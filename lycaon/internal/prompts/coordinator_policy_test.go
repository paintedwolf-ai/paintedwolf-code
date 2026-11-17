package prompts_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/toolsurface"
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
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "implement_investigate",
		Profiles:  toolProfiles(t),
		Plan:      toolsurface.Compile(sticky, deferred),
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "NewReader", err)
	vars := reader.Facts()
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

func TestCoordinatorPolicyFactKeys(t *testing.T) {
	reader, err := prompts.DeclaredReader(nil, "implement_investigate")
	testutil.FailErr(t, "DeclaredReader", err)
	vars := reader.Facts()
	for _, key := range []string{
		"is_coordinator",
		"lane_s_sync_tools",
		"more_tools_loadable",
		"recall_may_widen",
		"visual_review_cadence",
	} {
		if _, ok := vars[key]; !ok {
			t.Fatalf("missing reader fact %q", key)
		}
	}
	policy := spawn.PolicyTemplateVars()
	for _, key := range []string{
		"max_in_flight",
		"max_read_workers",
		"max_write_workers",
		"max_concurrent_tool_calls",
		"worker_summary_max_chars",
		"hunt_wave_workers",
	} {
		if _, ok := policy[key]; !ok {
			t.Fatalf("missing policy var %q", key)
		}
	}
	if policy["max_concurrent_tool_calls"] != spawn.MaxConcurrentToolCalls {
		t.Fatalf("max_concurrent_tool_calls mismatch")
	}
	if policy["worker_summary_max_chars"] != limits.DefaultWorkerSummaryMaxChars {
		t.Fatalf("worker_summary_max_chars = %v want %d", policy["worker_summary_max_chars"], limits.DefaultWorkerSummaryMaxChars)
	}
}
