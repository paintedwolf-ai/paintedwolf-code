package oar

import (
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func stepRule(steps ...NextStep) *Rule {
	return &Rule{ID: "R", NextSteps: &NextSteps{Steps: steps}}
}

func reachContext(request toolreach.Request) *GuardContext {
	gc := NewGuardContext()
	gc.Reach = request
	gc.ObservationData = map[string]any{"path": "src dir"}
	gc.ObserveToolCall("read", nil)
	return gc
}

func allPrograms(string) bool { return true }

func compileDeferred(t *testing.T, names ...string) toolsurface.Plan {
	t.Helper()
	return toolsurface.Compile(nil, names)
}

func TestResolveNextStepsOffersOnlyWhatTheRequestReaches(t *testing.T) {
	rule := stepRule(
		NextStep{Tool: "recall", Say: "Recover with {{ call }}.", Otherwise: "Re-observe it."},
		NextStep{Tool: "process_list", Complete: true, Say: "List with {{ call }}."},
	)
	for _, tc := range []struct {
		name    string
		request toolreach.Request
		want    string
		calls   []string
	}{
		{"offered", toolreach.Offered([]string{"recall", "process_list"}), "Recover with `recall(…)`. List with `process_list()`.", []string{"recall", "process_list"}},
		{"otherwise replaces an unreachable step", toolreach.Offered([]string{"process_list"}), "Re-observe it. List with `process_list()`.", []string{"process_list"}},
		{"no request reaches nothing", toolreach.Request{}, "Re-observe it.", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveNextSteps(rule, reachContext(tc.request))
			if got.Text != tc.want {
				t.Fatalf("text = %q, want %q", got.Text, tc.want)
			}
			var names []string
			for _, call := range got.Calls {
				names = append(names, call.Tool)
			}
			if !slices.Equal(names, tc.calls) {
				t.Fatalf("calls = %v, want %v", names, tc.calls)
			}
		})
	}
}

func TestResolveNextStepsChecksProgramsAsWellAsCommand(t *testing.T) {
	rule := stepRule(NextStep{Program: []string{"ls", "{{ paintedwolf.path }}"}, Complete: true, Say: "List with {{ call }}.", Otherwise: "Report it."})
	offered := toolreach.Offered([]string{"command"})
	for _, tc := range []struct {
		name    string
		request toolreach.Request
		want    string
	}{
		{"runnable", offered.WithPrograms(allPrograms), "List with `command(command=\"ls 'src dir'\")`."},
		{"denied by the profile or surface", offered.WithPrograms(func(string) bool { return false }), "Report it."},
		{"no program answer", offered, "Report it."},
		{"command not offered", toolreach.Offered(nil).WithPrograms(allPrograms), "Report it."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveNextSteps(rule, reachContext(tc.request)).Text; got != tc.want {
				t.Fatalf("text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveNextStepsTriesAlternativesInOrder(t *testing.T) {
	rule := stepRule(NextStep{
		Tool: "recall", Say: "Recall with {{ call }}.", Otherwise: "Nothing can recover it.",
		Or: []NextStep{{Tool: "view_image", Say: "View with {{ call }}."}},
	})
	if got := ResolveNextSteps(rule, reachContext(toolreach.Offered([]string{"view_image"}))).Text; got != "View with `view_image(…)`." {
		t.Fatalf("alternative = %q", got)
	}
	if got := ResolveNextSteps(rule, reachContext(toolreach.Offered(nil))).Text; got != "Nothing can recover it." {
		t.Fatalf("otherwise = %q", got)
	}
}

func TestResolveNextStepsWillNotOfferACompleteCallMissingAValue(t *testing.T) {
	gc := reachContext(toolreach.Offered([]string{"wait", "command"}).WithPrograms(allPrograms))
	gc.ObservationData = map[string]any{}
	rule := stepRule(
		NextStep{Tool: "wait", Complete: true, Args: map[string]any{"reason": "{{ paintedwolf.path }}"}, Say: "Wait with {{ call }}."},
		NextStep{Program: []string{"ls", "{{ paintedwolf.path }}"}, Complete: true, Say: "List with {{ call }}."},
	)
	if got := ResolveNextSteps(rule, gc); got.Text != "" || len(got.Calls) != 0 {
		t.Fatalf("resolved = %+v", got)
	}
}

func TestResolveNextStepsLoadsADeferredCallFirst(t *testing.T) {
	plan := compileDeferred(t, "recall")
	got := ResolveNextSteps(stepRule(NextStep{Tool: "recall", Say: "Recover with {{ call }}."}), reachContext(toolreach.Freeze([]string{toolreach.LoaderTool}, plan)))
	if len(got.Calls) != 2 || got.Calls[0].Tool != toolreach.LoaderTool || got.Calls[1].Tool != "recall" {
		t.Fatalf("calls = %+v", got.Calls)
	}
	if !strings.Contains(got.Text, "after loading it with") {
		t.Fatalf("text = %q", got.Text)
	}
}

func TestNextCallsDetailsLeavesOtherDetailsAlone(t *testing.T) {
	base := map[string]any{"path": "a"}
	merged := NextCallsDetails(base, map[string]any{NextCallsDataKey: []toolreach.Call{{Tool: "recall"}}})
	if _, leaked := base[NextCallsDataKey]; leaked || merged["path"] != "a" || merged[NextCallsDataKey] == nil {
		t.Fatalf("base = %v merged = %v", base, merged)
	}
	if got := NextCallsDetails(base, nil); got["path"] != "a" || len(got) != 1 {
		t.Fatalf("no calls = %v", got)
	}
}
