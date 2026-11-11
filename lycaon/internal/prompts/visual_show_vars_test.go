package prompts_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMergeVisualShowVarsFollowOfferedTools(t *testing.T) {
	into := map[string]any{}
	prompts.MergeVisualShowVars(nil, nil, into)
	if into["visual_show_available"] != false {
		t.Fatalf("empty names must not advertise show: %#v", into)
	}

	page := map[string]any{}
	prompts.MergeVisualShowVars([]string{"capture_page", "read"}, nil, page)
	if page["visual_show_available"] != true || page["visual_show_page"] != true || page["visual_show_terminal"] != false {
		t.Fatalf("offered capture must advertise page show only: %#v", page)
	}

	terminal := map[string]any{}
	prompts.MergeVisualShowVars([]string{"command"}, nil, terminal)
	if terminal["visual_show_available"] != true || terminal["visual_show_terminal"] != true || terminal["visual_show_page"] != false {
		t.Fatalf("command terminal_capture must advertise terminal show: %#v", terminal)
	}

	held := map[string]any{}
	prompts.MergeVisualShowVars([]string{"terminal_snapshot"}, nil, held)
	if held["visual_show_terminal"] != true {
		t.Fatalf("held capture satisfies terminal show: %#v", held)
	}

	unrelated := map[string]any{}
	prompts.MergeVisualShowVars([]string{"render_view", "scan_list"}, nil, unrelated)
	if unrelated["visual_show_available"] != false {
		t.Fatalf("render_view authors intent, not a running capture: %#v", unrelated)
	}
}

func TestMergeVisualShowVarsAskForLoadablePageCapture(t *testing.T) {
	loadable := map[string]any{}
	prompts.MergeVisualShowVars([]string{"read", "request_tools"}, []string{"capture_page", "page_open", "write"}, loadable)
	if loadable["visual_show_needs_request"] != true || loadable["visual_show_available"] != true || loadable["visual_show_page"] != false {
		t.Fatalf("loadable page capture must ask for the tools without teaching them: %#v", loadable)
	}

	readOnly := map[string]any{}
	prompts.MergeVisualShowVars([]string{"read", "request_tools"}, []string{"capture_page", "page_open"}, readOnly)
	if readOnly["visual_show_needs_request"] != false || readOnly["visual_show_available"] != false {
		t.Fatalf("an agent that cannot change files builds nothing to capture: %#v", readOnly)
	}

	noRequest := map[string]any{}
	prompts.MergeVisualShowVars([]string{"read", "write"}, []string{"capture_page"}, noRequest)
	if noRequest["visual_show_needs_request"] != false || noRequest["visual_show_available"] != false {
		t.Fatalf("without request_tools a deferred capture cannot load: %#v", noRequest)
	}

	loaded := map[string]any{}
	prompts.MergeVisualShowVars([]string{"capture_page", "request_tools"}, []string{"page_open"}, loaded)
	if loaded["visual_show_needs_request"] != false || loaded["visual_show_page"] != true {
		t.Fatalf("a loaded capture tool needs no request: %#v", loaded)
	}
}

func TestMergeVisualShowVarsAskForLoadableHeldTerminal(t *testing.T) {
	beside := map[string]any{}
	prompts.MergeVisualShowVars([]string{"command", "write", "request_tools"}, []string{"terminal_open", "terminal_send", "terminal_read", "terminal_snapshot"}, beside)
	if beside["visual_show_terminal"] != true || beside["visual_show_terminal_needs_request"] != true {
		t.Fatalf("one-shot capture beside a loadable held terminal must offer to load it: %#v", beside)
	}

	alone := map[string]any{}
	prompts.MergeVisualShowVars([]string{"read", "request_tools"}, []string{"terminal_open", "terminal_snapshot", "write"}, alone)
	if alone["visual_show_terminal_needs_request"] != true || alone["visual_show_available"] != true || alone["visual_show_terminal"] != false {
		t.Fatalf("a loadable held terminal must ask for the tools without teaching them: %#v", alone)
	}

	readOnly := map[string]any{}
	prompts.MergeVisualShowVars([]string{"read", "request_tools"}, []string{"terminal_open", "terminal_snapshot"}, readOnly)
	if readOnly["visual_show_terminal_needs_request"] != false || readOnly["visual_show_available"] != false {
		t.Fatalf("an agent that cannot change files builds nothing to drive: %#v", readOnly)
	}

	partial := map[string]any{}
	prompts.MergeVisualShowVars([]string{"write", "request_tools"}, []string{"terminal_open"}, partial)
	if partial["visual_show_terminal_needs_request"] != false {
		t.Fatalf("a terminal without snapshot cannot capture: %#v", partial)
	}

	loaded := map[string]any{}
	prompts.MergeVisualShowVars([]string{"terminal_open", "terminal_snapshot", "write", "request_tools"}, nil, loaded)
	if loaded["visual_show_terminal_needs_request"] != false || loaded["visual_show_terminal"] != true {
		t.Fatalf("a loaded held terminal needs no request: %#v", loaded)
	}
}

func TestMergeCoordinatorPromptVarsInvestigateFloorAsksForCapture(t *testing.T) {
	into := map[string]any{}
	err := prompts.MergeCoordinatorPromptVars("implement_investigate", prompts.ExecutionModePromptTransition{
		ExecutionMode: "investigate",
	}, "", into)
	testutil.FailErr(t, "merge investigate prompt vars", err)
	if into["visual_show_page"] != false || into["visual_show_needs_request"] != true || into["visual_show_available"] != true {
		t.Fatalf("the investigate floor carries no capture tool but must ask to load one: %#v", into)
	}
	loaded := map[string]any{"surface_offered": []string{"read", "capture_page", "request_tools"}}
	err = prompts.MergeCoordinatorPromptVars("implement_investigate", prompts.ExecutionModePromptTransition{
		ExecutionMode: "investigate",
	}, "", loaded)
	testutil.FailErr(t, "merge loaded prompt vars", err)
	if loaded["visual_show_available"] != true || loaded["visual_show_page"] != true {
		t.Fatalf("a loaded capture tool must advertise page show: %#v", loaded)
	}
}

func TestMergeCoordinatorPromptVarsSynthesisDoesNotRequireRuntimeCapture(t *testing.T) {
	into := map[string]any{}
	err := prompts.MergeCoordinatorPromptVars("implement_synthesis", prompts.ExecutionModePromptTransition{
		ExecutionMode: "wrapup",
	}, "", into)
	testutil.FailErr(t, "merge synthesis prompt vars", err)
	if into["visual_show_available"] != false || into["visual_show_page"] != false || into["visual_show_terminal"] != false {
		t.Fatalf("render_view authors intent, not a running interface capture: %#v", into)
	}
}
