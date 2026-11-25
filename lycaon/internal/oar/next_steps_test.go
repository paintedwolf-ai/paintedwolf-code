package oar

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"

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
		NextStep{Tool: "recall", Say: "Recover with {% if load %}{{ load }}, then {% endif %}{{ call }}.", Otherwise: "Re-observe it."},
		NextStep{Tool: "process_list", Complete: true, Say: "List with {% if load %}{{ load }}, then {% endif %}{{ call }}."},
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
	rule := stepRule(NextStep{Program: []string{"ls", "{{ paintedwolf.path }}"}, Complete: true, Say: "List with {% if load %}{{ load }}, then {% endif %}{{ call }}.", Otherwise: "Report it."})
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
		Tool: "recall", Say: "Recall with {% if load %}{{ load }}, then {% endif %}{{ call }}.", Otherwise: "Nothing can recover it.",
		Or: []NextStep{{Tool: "view_image", Say: "View with {% if load %}{{ load }}, then {% endif %}{{ call }}."}},
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
		NextStep{Tool: "wait", Complete: true, Args: map[string]any{"reason": "{{ paintedwolf.path }}"}, Say: "Wait with {% if load %}{{ load }}, then {% endif %}{{ call }}."},
		NextStep{Program: []string{"ls", "{{ paintedwolf.path }}"}, Complete: true, Say: "List with {% if load %}{{ load }}, then {% endif %}{{ call }}."},
	)
	if got := ResolveNextSteps(rule, gc); got.Text != "" || len(got.Calls) != 0 {
		t.Fatalf("resolved = %+v", got)
	}
}

func TestResolveNextStepsBindsTheLoaderOnlyWhenTheCallNeedsIt(t *testing.T) {
	// The say exposes the bindings with a separator so the test reads values, not copy.
	rule := stepRule(NextStep{Tool: "recall", Say: "{% if load %}{{ load }}|{% endif %}{{ call }}"})

	deferred := ResolveNextSteps(rule, reachContext(toolreach.Freeze([]string{toolreach.LoaderTool}, compileDeferred(t, "recall"))))
	if len(deferred.Calls) != 2 || deferred.Calls[0].Tool != toolreach.LoaderTool || deferred.Calls[1].Tool != "recall" {
		t.Fatalf("deferred calls = %+v", deferred.Calls)
	}
	if want := deferred.Calls[0].Text() + "|" + deferred.Calls[1].Text(); deferred.Text != want {
		t.Fatalf("deferred bindings = %q, want load=%q call=%q", deferred.Text, deferred.Calls[0].Text(), deferred.Calls[1].Text())
	}

	offered := ResolveNextSteps(rule, reachContext(toolreach.Offered([]string{"recall"})))
	if len(offered.Calls) != 1 || offered.Calls[0].Tool != "recall" {
		t.Fatalf("offered calls = %+v", offered.Calls)
	}
	if offered.Text != offered.Calls[0].Text() {
		t.Fatalf("offered bindings = %q, want only call=%q", offered.Text, offered.Calls[0].Text())
	}
}

func TestValidateNextStepsRequiresCopyForBothLoaderStates(t *testing.T) {
	loader, err := NewLoader("")
	testutil.FailErr(t, "load OAR", err)
	for say, valid := range map[string]bool{
		"{% if load %}{{ load }}, then {% endif %}{{ call }}":                       true,
		"{% if not load %}{{ call }}{% else %}{{ load }} and {{ call }}{% endif %}": true,
		"{{ call }}":                  false,
		"{{ load }}, then {{ call }}": false,
		"{% if load %}{{ load }}, then {{ call }}{% endif %}":                                false,
		"{% if load %}{{ load }}{% endif %}{% if not load %}{{ load }}{% endif %}{{ call }}": false,
		"{% if load %}{{ load }}{% endif %}":                                                 false,
	} {
		err := loader.validateNextSteps(stepRule(NextStep{Tool: "recall", Say: say}))
		if valid && err != nil {
			t.Errorf("%q rejected: %v", say, err)
		}
		if !valid && !errors.Is(err, ErrStepLoadBranch) {
			t.Errorf("%q: err = %v, want ErrStepLoadBranch", say, err)
		}
	}
	if err := loader.validateNextSteps(stepRule(NextStep{Tool: "recall", Say: "{% if load %}{{ call }}"})); err == nil || errors.Is(err, ErrStepLoadBranch) {
		t.Errorf("unterminated say: err = %v, want a parse error", err)
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
			Steps: []NextStep{{When: `paintedwolf.next_steps != ""`, Tool: "recall", Say: "Use {% if load %}{{ load }}, then {% endif %}{{ call }}."}},
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
			{Tool: "recall", Args: map[string]any{"overlay_id": "x"}, Say: "Use {% if load %}{{ load }}, then {% endif %}{{ call }}."},
		}}}, "recall does not accept overlay_id"},
		{"reissue path no tool accepts", &Rule{ID: "R", NextSteps: &NextSteps{Steps: []NextStep{
			{Args: map[string]any{"bogus": "x"}, Say: "Use {% if load %}{{ load }}, then {% endif %}{{ call }}."},
		}}}, "no declared tool accepts bogus"},
		{"reissue selector must accept", &Rule{ID: "R", Selector: Selector{"tools": {"recall"}}, NextSteps: &NextSteps{Steps: []NextStep{
			{Args: map[string]any{"overlay_id": "x"}, Say: "Use {% if load %}{{ load }}, then {% endif %}{{ call }}."},
		}}}, "recall does not accept overlay_id"},
		{"pick path checked", &Rule{ID: "R", RequiresFacts: []string{"paintedwolf.x"}, NextSteps: &NextSteps{Steps: []NextStep{
			{Tool: "recall", Pick: map[string]string{"bogus": "paintedwolf.x"}, Say: "Use {% if load %}{{ load }}, then {% endif %}{{ call }}."},
		}}}, "recall does not accept bogus"},
		{"or alternatives checked", &Rule{ID: "R", NextSteps: &NextSteps{Steps: []NextStep{
			{Tool: "recall", Say: "Use {% if load %}{{ load }}, then {% endif %}{{ call }}.", Or: []NextStep{
				{Args: map[string]any{"bogus": "x"}, Say: "Use {% if load %}{{ load }}, then {% endif %}{{ call }}."},
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
		{Tool: "recall", Args: map[string]any{"query": "{{ paintedwolf.command }}"}, Say: "Use {% if load %}{{ load }}, then {% endif %}{{ call }}."},
		{Args: map[string]any{"overlay_id": "x"}, Say: "Use {% if load %}{{ load }}, then {% endif %}{{ call }}."},
	}}}
	if err := loader.validateNextSteps(rule); err != nil {
		t.Fatalf("rejected schema-valid steps: %v", err)
	}
}

func TestValidateNextStepsSkipsArgCheckWithoutSchemas(t *testing.T) {
	loader, err := NewLoader("")
	testutil.FailErr(t, "load OAR", err)
	rule := &Rule{ID: "R", NextSteps: &NextSteps{Steps: []NextStep{
		{Args: map[string]any{"bogus": "x"}, Say: "Use {% if load %}{{ load }}, then {% endif %}{{ call }}."},
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
					Say: "Call {% if load %}{{ load }}, then {% endif %}{{ call }} now.",
				},
				{
					When: "paintedwolf.handle == ''",
					Tool: "wait",
					Args: map[string]any{"until_complete": false},
					Say:  "Park via {% if load %}{{ load }}, then {% endif %}{{ call }}.",
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
		Say: "Call {% if load %}{{ load }}, then {% endif %}{{ call }}.",
	})
	if suppressed := ResolveNextSteps(missingNestedRule, gcMissing); suppressed.Text != "" || len(suppressed.Calls) != 0 {
		t.Fatalf("complete step with missing nested template should be suppressed, got %+v", suppressed)
	}
}
