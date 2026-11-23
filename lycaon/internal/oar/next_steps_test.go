package oar

import (
	"github.com/lycaon/lycaon/pkg/api"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolschema"
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
	merged := NextCallsDetails(base, map[string]any{api.ToolFeedbackNextCalls: []toolreach.Call{{Tool: "recall"}}})
	if _, leaked := base[api.ToolFeedbackNextCalls]; leaked || merged["path"] != "a" || merged[api.ToolFeedbackNextCalls] == nil {
		t.Fatalf("base = %v merged = %v", base, merged)
	}
	if got := NextCallsDetails(base, nil); got["path"] != "a" || len(got) != 1 {
		t.Fatalf("no calls = %v", got)
	}
}

func TestValidateNextStepsRejectsDecisionReadsAcrossAllConditionSurfaces(t *testing.T) {
	loader, err := NewLoader("")
	testutil.FailErr(t, "load OAR", err)
	for _, tc := range []struct {
		name string
		rule *Rule
	}{
		{"rule when", &Rule{ID: "R", When: `paintedwolf.next_steps != ""`}},
		{"rule flow", &Rule{ID: "R", Flow: []string{`paintedwolf.next_steps != ""`}}},
		{"step when", &Rule{ID: "R", RequiresFacts: []string{NextStepsFact}, NextSteps: &NextSteps{
			Steps: []NextStep{{When: `paintedwolf.next_steps != ""`, Tool: "recall", Say: "Use {{ call }}."}},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := loader.validateNextSteps(tc.rule); err == nil {
				t.Fatalf("accepted %s reading %s", tc.name, NextStepsFact)
			}
		})
	}
}

func schemaTestConfig() *toolschema.Config {
	return &toolschema.Config{Tools: map[string]toolschema.Entry{
		"recall":  {Schema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}},
		"preview": {Schema: map[string]any{"type": "object", "properties": map[string]any{"overlay_id": map[string]any{"type": "string"}}}},
	}}
}

func TestValidateNextStepsChecksArgsAgainstToolSchemas(t *testing.T) {
	loader, err := NewLoader("")
	testutil.FailErr(t, "load OAR", err)
	loader.SetToolSchemas(schemaTestConfig())
	for _, tc := range []struct {
		name    string
		rule    *Rule
		wantErr string
	}{
		{"explicit tool unknown path", &Rule{ID: "R", NextSteps: &NextSteps{Steps: []NextStep{
			{Tool: "recall", Args: map[string]any{"overlay_id": "x"}, Say: "Use {{ call }}."},
		}}}, "recall does not accept overlay_id"},
		{"reissue path no tool accepts", &Rule{ID: "R", NextSteps: &NextSteps{Steps: []NextStep{
			{Args: map[string]any{"bogus": "x"}, Say: "Use {{ call }}."},
		}}}, "no declared tool accepts bogus"},
		{"reissue selector must accept", &Rule{ID: "R", Selector: Selector{"tools": {"recall"}}, NextSteps: &NextSteps{Steps: []NextStep{
			{Args: map[string]any{"overlay_id": "x"}, Say: "Use {{ call }}."},
		}}}, "recall does not accept overlay_id"},
		{"pick path checked", &Rule{ID: "R", RequiresFacts: []string{"paintedwolf.x"}, NextSteps: &NextSteps{Steps: []NextStep{
			{Tool: "recall", Pick: map[string]string{"bogus": "paintedwolf.x"}, Say: "Use {{ call }}."},
		}}}, "recall does not accept bogus"},
		{"or alternatives checked", &Rule{ID: "R", NextSteps: &NextSteps{Steps: []NextStep{
			{Tool: "recall", Say: "Use {{ call }}.", Or: []NextStep{
				{Args: map[string]any{"bogus": "x"}, Say: "Use {{ call }}."},
			}},
		}}}, "no declared tool accepts bogus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := loader.validateNextSteps(tc.rule)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateNextStepsAcceptsSchemaValidArgs(t *testing.T) {
	loader, err := NewLoader("")
	testutil.FailErr(t, "load OAR", err)
	loader.SetToolSchemas(schemaTestConfig())
	rule := &Rule{ID: "R", RequiresFacts: []string{"paintedwolf.command"}, NextSteps: &NextSteps{Steps: []NextStep{
		{Tool: "recall", Args: map[string]any{"query": "{{ paintedwolf.command }}"}, Say: "Use {{ call }}."},
		{Args: map[string]any{"overlay_id": "x"}, Say: "Use {{ call }}."},
	}}}
	if err := loader.validateNextSteps(rule); err != nil {
		t.Fatalf("rejected schema-valid steps: %v", err)
	}
}

func TestValidateNextStepsSkipsArgCheckWithoutSchemas(t *testing.T) {
	loader, err := NewLoader("")
	testutil.FailErr(t, "load OAR", err)
	rule := &Rule{ID: "R", NextSteps: &NextSteps{Steps: []NextStep{
		{Args: map[string]any{"bogus": "x"}, Say: "Use {{ call }}."},
	}}}
	if err := loader.validateNextSteps(rule); err != nil {
		t.Fatalf("nil schemas must skip the arg check: %v", err)
	}
}

func TestResolveNextStepsFormatsNestedWaitConditionsAndPartialCalls(t *testing.T) {
	waitRule := &Rule{
		ID:            "WAIT_TEST",
		RequiresFacts: []string{"paintedwolf.handle"},
		NextSteps: &NextSteps{
			Steps: []NextStep{
				{
					When:     "paintedwolf.handle != ''",
					Tool:     "wait",
					Complete: true,
					Args: map[string]any{
						"conditions": []any{
							map[string]any{
								"kind":    "process_done",
								"handles": []any{"{{ paintedwolf.handle }}"},
							},
						},
						"reason":         "waiting on \"job\"\nnext",
						"timeout_ms":     60000,
						"until_complete": true,
					},
					Say: "Call {{ call }} now.",
				},
				{
					When: "paintedwolf.handle == ''",
					Tool: "wait",
					Args: map[string]any{"until_complete": false},
					Say:  "Park via {{ call }}.",
				},
			},
		},
	}

	loader, err := NewLoader("")
	testutil.FailErr(t, "load OAR", err)
	testutil.FailErr(t, "validate wait rule", loader.validateNextSteps(waitRule))

	gc := reachContext(toolreach.Offered([]string{"wait"}))
	gc.ObservationData["handle"] = "cmd-docker-1"
	got := ResolveNextSteps(waitRule, gc)
	want := "Call `wait(conditions=[{\"handles\": [\"cmd-docker-1\"], \"kind\": \"process_done\"}], reason=\"waiting on \\\"job\\\"\\nnext\", timeout_ms=60000, until_complete=true)` now."
	if got.Text != want {
		t.Fatalf("ResolveNextSteps text =\n%s\nwant:\n%s", got.Text, want)
	}

	gcMissing := reachContext(toolreach.Offered([]string{"wait"}))
	gotPartial := ResolveNextSteps(waitRule, gcMissing)
	if gotPartial.Text != "Park via `wait(until_complete=false, …)`." {
		t.Fatalf("partial ResolveNextSteps text = %q", gotPartial.Text)
	}

	missingNestedRule := stepRule(NextStep{
		Tool:     "wait",
		Complete: true,
		Args: map[string]any{
			"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"{{ paintedwolf.missing_handle }}"}}},
		},
		Say: "Call {{ call }}.",
	})
	if suppressed := ResolveNextSteps(missingNestedRule, gcMissing); suppressed.Text != "" || len(suppressed.Calls) != 0 {
		t.Fatalf("complete step with missing nested template should be suppressed, got %+v", suppressed)
	}
}

func TestResolveNextStepsPreservesStepIdentityAndObserverProvenance(t *testing.T) {
	rule := &Rule{
		Namespace:     "painted-wolf/security",
		ID:            "CRED_STEP_PROVENANCE",
		Anchor:        AnchorCredentialAssignment,
		RequiresFacts: []string{"paintedwolf.listed"},
		NextSteps: &NextSteps{
			Steps: []NextStep{
				{
					When:      "paintedwolf.listed",
					Tool:      "secret_generate",
					Args:      map[string]any{"format": "base64url", "scope": "chat"},
					Say:       "For a new credential, use {{ call }} and carry its returned reference.",
					Otherwise: "Report protected generation need.",
				},
				{
					Tool: "secret_generate",
					Args: map[string]any{"format": "base64url", "scope": "chat"},
					Say:  "For a new credential, use {{ call }} and carry its returned reference.",
					Or: []NextStep{
						{
							Tool: "ask_user",
							Say:  "Fallback to {{ call }}.",
						},
					},
				},
				{
					Tool:      "recall",
					Say:       "Recall via {{ call }}.",
					Otherwise: "No recall or view available.",
					Or: []NextStep{
						{
							Tool: "view_image",
							Say:  "Inspect via {{ call }}.",
						},
						{
							Tool: "ask_user",
							Say:  "Ask via {{ call }}.",
						},
					},
				},
				{
					Tool:      "task",
					Say:       "Dispatch via {{ call }}.",
					Otherwise: "Escalation unavailable.",
				},
			},
		},
	}

	newEvalContext := func() *GuardContext {
		gc := reachContext(toolreach.Offered([]string{"secret_generate", "view_image"}))
		gc.Published["paintedwolf.listed"] = false
		return gc
	}
	recordBranches := func(fn func()) map[string]RuleBranchHit {
		rec := &RuleBranchRecorder{Hits: map[string]*RuleBranchHit{}}
		prev := ActiveRuleBranchRecorder.Swap(rec)
		defer ActiveRuleBranchRecorder.Store(prev)
		fn()
		out := make(map[string]RuleBranchHit, len(rec.Hits))
		for k, v := range rec.Hits {
			out[k] = *v
		}
		return out
	}

	var unobserved ResolvedSteps
	unobservedBranches := recordBranches(func() {
		unobserved = ResolveNextSteps(rule, newEvalContext())
	})
	if unobserved.Resolutions != nil || len(unobserved.Resolutions) != 0 {
		t.Fatalf("ordinary evaluation without observer must collect 0 resolutions, got %+v", unobserved.Resolutions)
	}

	var observed []ResolvedSteps
	key := new(int)
	ActiveNextStepObservers.Store(key, NextStepObserver(func(gotRule *Rule, resolved ResolvedSteps) {
		if gotRule == rule {
			observed = append(observed, resolved)
		}
	}))
	defer ActiveNextStepObservers.Delete(key)

	var got ResolvedSteps
	observedBranches := recordBranches(func() {
		got = ResolveNextSteps(rule, newEvalContext())
	})
	if got.Text != unobserved.Text {
		t.Fatalf("observed vs ordinary text mismatch:\nobserved:   %q\nunobserved: %q", got.Text, unobserved.Text)
	}
	if len(got.Calls) != len(unobserved.Calls) {
		t.Fatalf("observed vs ordinary calls len mismatch: %+v vs %+v", got.Calls, unobserved.Calls)
	}
	for i := range got.Calls {
		if got.Calls[i].Text() != unobserved.Calls[i].Text() {
			t.Fatalf("call[%d] mismatch: %q vs %q", i, got.Calls[i].Text(), unobserved.Calls[i].Text())
		}
	}
	if len(observedBranches) != len(unobservedBranches) {
		t.Fatalf("branch coverage mismatch: observed=%+v unobserved=%+v", observedBranches, unobservedBranches)
	}
	for k, hit := range observedBranches {
		if unobservedBranches[k] != hit {
			t.Fatalf("branch[%s] mismatch: observed=%+v unobserved=%+v", k, hit, unobservedBranches[k])
		}
	}

	if len(observed) != 1 {
		t.Fatalf("expected 1 observed evaluation, got %d", len(observed))
	}
	gcScenario := newEvalContext()
	gcScenario.FromScenario = true
	scenarioGot := ResolveNextSteps(rule, gcScenario)
	if len(observed) != 1 {
		t.Fatalf("scenario evaluation must not notify observer, got %d", len(observed))
	}
	if scenarioGot.Resolutions != nil || len(scenarioGot.Resolutions) != 0 {
		t.Fatalf("scenario evaluation must collect 0 resolutions, got %+v", scenarioGot.Resolutions)
	}
	if scenarioGot.Text != unobserved.Text || len(scenarioGot.Calls) != len(unobserved.Calls) {
		t.Fatalf("scenario text/calls mismatch: %+v vs %+v", scenarioGot, unobserved)
	}

	wantOutcomes := []struct {
		prefix  string
		outcome StepResolutionOutcome
		calls   int
		hasText bool
	}{
		{"steps[0]", StepOutcomeDisabled, 0, false},
		{"steps[1]", StepOutcomeEmittedCall, 1, true},
		{"steps[1].or[0]", StepOutcomeUnselected, 0, false},
		{"steps[2]", StepOutcomeOmitted, 0, false},
		{"steps[2].or[0]", StepOutcomeEmittedCall, 1, true},
		{"steps[2].or[1]", StepOutcomeUnselected, 0, false},
		{"steps[3]", StepOutcomeEmittedOtherwise, 0, true},
	}
	if len(got.Resolutions) != len(wantOutcomes) {
		t.Fatalf("resolutions len = %d, want %d (%+v)", len(got.Resolutions), len(wantOutcomes), got.Resolutions)
	}
	for i, w := range wantOutcomes {
		res := got.Resolutions[i]
		if res.QualifiedRule != rule.Qualified() || res.Anchor != rule.Anchor || res.RuleID != rule.ID {
			t.Fatalf("res[%d] identity = %+v", i, res)
		}
		if res.BranchPrefix != w.prefix || res.Outcome != w.outcome || len(res.SelectedCalls) != w.calls || (res.RenderedText != "") != w.hasText {
			t.Fatalf("res[%d] = %+v, want %+v", i, res, w)
		}
	}
}

func TestNextStepObserversAreIndependentRegistrations(t *testing.T) {
	rule := stepRule(NextStep{Tool: "view_image", Say: "View with {{ call }}."})
	gc := reachContext(toolreach.Offered([]string{"view_image"}))

	var first, second int
	firstKey, secondKey := new(int), new(int)
	ActiveNextStepObservers.Store(firstKey, NextStepObserver(func(got *Rule, _ ResolvedSteps) {
		if got == rule {
			first++
		}
	}))
	ActiveNextStepObservers.Store(secondKey, NextStepObserver(func(got *Rule, _ ResolvedSteps) {
		if got == rule {
			second++
		}
	}))
	defer ActiveNextStepObservers.Delete(secondKey)

	ResolveNextSteps(rule, gc)
	ActiveNextStepObservers.Delete(firstKey)
	ResolveNextSteps(rule, gc)

	if first != 1 || second != 2 {
		t.Fatalf("observer counts first=%d second=%d, want 1 and 2", first, second)
	}
}
