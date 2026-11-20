package prompts_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func visualShowFacts(t *testing.T, offered, requestable []string) map[string]any {
	t.Helper()
	tools := make(map[string]bool, len(offered)+len(requestable))
	for _, name := range offered {
		tools[name] = true
	}
	for _, name := range requestable {
		tools[name] = true
	}
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "worker",
		Plan:      toolsurface.Compile(offered, requestable),
		Profiles: []sandbox.ToolProfile{{
			ID:             "worker",
			Tools:          tools,
			CommandEffects: toolcontract.AllCommandEffects(),
		}},
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "NewReader", err)
	return reader.Facts()
}

func TestVisualShowFactsFollowOfferedTools(t *testing.T) {
	into := visualShowFacts(t, nil, nil)
	if into["visual_show_available"] != false {
		t.Fatalf("empty names must not advertise show: %#v", into)
	}

	page := visualShowFacts(t, []string{"capture_page", "read"}, nil)
	if page["visual_show_available"] != true || page["visual_show_page"] != true || page["visual_show_terminal"] != false {
		t.Fatalf("offered capture must advertise page show only: %#v", page)
	}

	terminal := visualShowFacts(t, []string{"command"}, nil)
	if terminal["visual_show_available"] != true || terminal["visual_show_terminal"] != true || terminal["visual_show_page"] != false {
		t.Fatalf("command terminal_capture must advertise terminal show: %#v", terminal)
	}

	open := visualShowFacts(t, []string{"command", "terminal_open", "write", "request_tools"}, []string{"terminal_send", "terminal_read", "terminal_snapshot", "terminal_close"})
	if open["visual_show_terminal"] != true || open["visual_show_terminal_needs_request"] != false {
		t.Fatalf("terminal_open captures its first screen, and its controls attach once open: %#v", open)
	}

	held := visualShowFacts(t, []string{"terminal_snapshot"}, nil)
	if held["visual_show_terminal"] != true {
		t.Fatalf("held capture satisfies terminal show: %#v", held)
	}

	unrelated := visualShowFacts(t, []string{"render_view", "scan_list"}, nil)
	if unrelated["visual_show_available"] != false {
		t.Fatalf("render_view authors intent, not a running capture: %#v", unrelated)
	}
}

func TestVisualShowFactsAskForLoadablePageCapture(t *testing.T) {
	loadable := visualShowFacts(t, []string{"read", "request_tools"}, []string{"capture_page", "page_open", "write"})
	if loadable["visual_show_needs_request"] != true || loadable["visual_show_available"] != true || loadable["visual_show_page"] != false {
		t.Fatalf("loadable page capture must ask for the tools without teaching them: %#v", loadable)
	}

	readOnly := visualShowFacts(t, []string{"read", "request_tools"}, []string{"capture_page", "page_open"})
	if readOnly["visual_show_needs_request"] != false || readOnly["visual_show_available"] != false {
		t.Fatalf("an agent that cannot change files builds nothing to capture: %#v", readOnly)
	}

	noRequest := visualShowFacts(t, []string{"read", "write"}, []string{"capture_page"})
	if noRequest["visual_show_needs_request"] != false || noRequest["visual_show_available"] != false {
		t.Fatalf("without request_tools a deferred capture cannot load: %#v", noRequest)
	}

	loaded := visualShowFacts(t, []string{"capture_page", "request_tools"}, []string{"page_open"})
	if loaded["visual_show_needs_request"] != false || loaded["visual_show_page"] != true {
		t.Fatalf("a loaded capture tool needs no request: %#v", loaded)
	}
}

func TestVisualShowFactsAskForLoadableHeldTerminal(t *testing.T) {
	beside := visualShowFacts(t, []string{"command", "write", "request_tools"}, []string{"terminal_open", "terminal_send", "terminal_read", "terminal_snapshot"})
	if beside["visual_show_terminal"] != true || beside["visual_show_terminal_needs_request"] != true {
		t.Fatalf("one-shot capture beside a loadable held terminal must offer to load it: %#v", beside)
	}

	alone := visualShowFacts(t, []string{"read", "request_tools"}, []string{"terminal_open", "terminal_snapshot", "write"})
	if alone["visual_show_terminal_needs_request"] != true || alone["visual_show_available"] != true || alone["visual_show_terminal"] != false {
		t.Fatalf("a loadable held terminal must ask for the tools without teaching them: %#v", alone)
	}

	readOnly := visualShowFacts(t, []string{"read", "request_tools"}, []string{"terminal_open", "terminal_snapshot"})
	if readOnly["visual_show_terminal_needs_request"] != false || readOnly["visual_show_available"] != false {
		t.Fatalf("an agent that cannot change files builds nothing to drive: %#v", readOnly)
	}

	partial := visualShowFacts(t, []string{"write", "request_tools"}, []string{"terminal_open"})
	if partial["visual_show_terminal_needs_request"] != false {
		t.Fatalf("a terminal without snapshot cannot capture: %#v", partial)
	}

	loaded := visualShowFacts(t, []string{"terminal_open", "terminal_snapshot", "write", "request_tools"}, nil)
	if loaded["visual_show_terminal_needs_request"] != false || loaded["visual_show_terminal"] != true {
		t.Fatalf("a loaded held terminal needs no request: %#v", loaded)
	}
}

func TestMergeCoordinatorPromptVarsInvestigateFloorAsksForCapture(t *testing.T) {
	reader, err := prompts.DeclaredReader(nil, "implement_investigate")
	testutil.FailErr(t, "DeclaredReader implement_investigate", err)
	into := reader.Facts()
	err = prompts.MergeCoordinatorPromptVars("implement_investigate", prompts.ExecutionModePromptTransition{}, "", into)
	testutil.FailErr(t, "merge investigate prompt vars", err)
	if into["visual_show_page"] != false || into["visual_show_needs_request"] != true || into["visual_show_available"] != true {
		t.Fatalf("the investigate floor carries no capture tool but must ask to load one: %#v", into)
	}
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	plan, err := prompts.CompileSurfacePlan("implement_investigate", 1)
	testutil.FailErr(t, "CompileSurfacePlan", err)
	loadedReader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "implement_investigate",
		Profiles:  profiles,
		Plan:      plan,
		Loaded:    map[string]bool{"capture_page": true},
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	})
	testutil.FailErr(t, "loaded reader", err)
	loaded := loadedReader.Facts()
	err = prompts.MergeCoordinatorPromptVars("implement_investigate", prompts.ExecutionModePromptTransition{}, "", loaded)
	testutil.FailErr(t, "merge loaded prompt vars", err)
	if loaded["visual_show_available"] != true || loaded["visual_show_page"] != true {
		t.Fatalf("a loaded capture tool must advertise page show: %#v", loaded)
	}
}

func TestMergeCoordinatorPromptVarsSynthesisDoesNotRequireRuntimeCapture(t *testing.T) {
	reader, err := prompts.DeclaredReader(nil, "implement_synthesis")
	testutil.FailErr(t, "DeclaredReader implement_synthesis", err)
	into := reader.Facts()
	err = prompts.MergeCoordinatorPromptVars("implement_synthesis", prompts.ExecutionModePromptTransition{}, "", into)
	testutil.FailErr(t, "merge synthesis prompt vars", err)
	if into["visual_show_available"] != false || into["visual_show_page"] != false || into["visual_show_terminal"] != false {
		t.Fatalf("render_view authors intent, not a running interface capture: %#v", into)
	}
}
