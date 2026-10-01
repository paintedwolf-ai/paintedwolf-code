package prompts_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMergeVisualShowVarsFollowOfferedTools(t *testing.T) {
	into := map[string]any{}
	prompts.MergeVisualShowVars(nil, into)
	if into["visual_show_available"] != false {
		t.Fatalf("empty names must not advertise show: %#v", into)
	}

	page := map[string]any{}
	prompts.MergeVisualShowVars([]string{"capture_page", "read"}, page)
	if page["visual_show_available"] != true || page["visual_show_page"] != true || page["visual_show_terminal"] != false {
		t.Fatalf("offered capture must advertise page show only: %#v", page)
	}

	terminal := map[string]any{}
	prompts.MergeVisualShowVars([]string{"command"}, terminal)
	if terminal["visual_show_available"] != true || terminal["visual_show_terminal"] != true || terminal["visual_show_page"] != false {
		t.Fatalf("command terminal_capture must advertise terminal show: %#v", terminal)
	}

	held := map[string]any{}
	prompts.MergeVisualShowVars([]string{"terminal_snapshot"}, held)
	if held["visual_show_terminal"] != true {
		t.Fatalf("held capture satisfies terminal show: %#v", held)
	}

	unrelated := map[string]any{}
	prompts.MergeVisualShowVars([]string{"render_view", "scan_list"}, unrelated)
	if unrelated["visual_show_available"] != false {
		t.Fatalf("render_view authors intent, not a running capture: %#v", unrelated)
	}
}

func TestMergeCoordinatorPromptVarsInvestigateFloorHasNoShow(t *testing.T) {
	into := map[string]any{}
	err := prompts.MergeCoordinatorPromptVars("implement_investigate", prompts.ExecutionModePromptTransition{
		ExecutionMode: "investigate",
	}, prompts.CoordinatorPromptGates{}, into)
	testutil.FailErr(t, "merge investigate prompt vars", err)
	if into["visual_show_available"] != false || into["more_tools_loadable"] != true {
		t.Fatalf("the investigate floor carries no capture tool but can load them: %#v", into)
	}
	loaded := map[string]any{"surface_offered": []string{"read", "capture_page", "request_tools"}}
	err = prompts.MergeCoordinatorPromptVars("implement_investigate", prompts.ExecutionModePromptTransition{
		ExecutionMode: "investigate",
	}, prompts.CoordinatorPromptGates{}, loaded)
	testutil.FailErr(t, "merge loaded prompt vars", err)
	if loaded["visual_show_available"] != true || loaded["visual_show_page"] != true {
		t.Fatalf("a loaded capture tool must advertise page show: %#v", loaded)
	}
}

func TestMergeCoordinatorPromptVarsSynthesisDoesNotRequireRuntimeCapture(t *testing.T) {
	into := map[string]any{}
	err := prompts.MergeCoordinatorPromptVars("implement_synthesis", prompts.ExecutionModePromptTransition{
		ExecutionMode: "wrapup",
	}, prompts.CoordinatorPromptGates{}, into)
	testutil.FailErr(t, "merge synthesis prompt vars", err)
	if into["visual_show_available"] != false || into["visual_show_page"] != false || into["visual_show_terminal"] != false {
		t.Fatalf("render_view authors intent, not a running interface capture: %#v", into)
	}
}
