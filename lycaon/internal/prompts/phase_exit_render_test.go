package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Every exit kind is rendered here. A kind with no matching template branch
// emits a workflow block that says nothing about how the phase ends.
func renderActiveWorkflow(t *testing.T, phaseExit map[string]any) string {
	t.Helper()
	vars := map[string]any{
		"workflow_id":   "demo",
		"phases":        []map[string]any{{"id": "one", "is_current": true}},
		"current_phase": "one",
		"phase_exit":    phaseExit,
	}
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.Render(context.Background(), "guidance/active-workflow.md", vars)
	testutil.FailErr(t, "render active-workflow guidance", err)
	if strings.Contains(out, "{%") || strings.Contains(out, "{{") {
		t.Fatalf("unrendered template syntax in output: %q", out)
	}
	return out
}

func TestPhaseExitRendersEveryKind(t *testing.T) {
	cases := []struct {
		name  string
		exit  map[string]any
		want  []string
		avoid []string
	}{
		{
			name: "terminal",
			exit: map[string]any{"kind": "terminal"},
			want: []string{"last phase"},
			// Terminal phases have no advance to talk about either way.
			avoid: []string{"workflow_advance"},
		},
		{
			name: "review_loop with owed reviewers",
			exit: map[string]any{
				"kind": "review_loop", "review_agents": []string{"skeptic", "web-researcher"},
				"review_loop_key": "survey_challenged", "review_loop_cap": 1,
				"verdict_schema": `{"verdict":"CHALLENGED","claims":"claims"}`, "coordinator_advances": true,
			},
			want: []string{"`skeptic`", "`web-researcher`", "one assistant message",
				"submit_verdict", "CHALLENGED", "evidence_passed:survey_challenged"},
		},
		{
			name: "review_loop with structured coverage and set-asides",
			exit: map[string]any{
				"kind":           "review_loop",
				"verdict_schema": `{"verdict":"CHALLENGED","challenges":"claims","coverage":"coverage_review","set_asides":"set_asides"}`,
			},
			want: []string{"`coverage_review` is an object with `revision` and `assessments`", "`set_asides` is an array"},
		},
		{
			name: "review_loop with no reviewers still names the verdict channel",
			exit: map[string]any{"kind": "review_loop", "review_loop_key": "claims"},
			want: []string{"submit_verdict", "evidence_passed:claims"},
		},
		{
			name:  "human_approval",
			exit:  map[string]any{"kind": "human_approval", "human_approval": true},
			want:  []string{"End the turn", "chat prose is not approval", "revision feedback"},
			avoid: []string{"review bar"},
		},
		{
			name: "invoke",
			exit: map[string]any{"kind": "invoke", "invoke_workflow_id": "child-flow"},
			want: []string{"child workflow", "`child-flow`", "child_run_complete"},
		},
		{
			name: "proof with open gates",
			exit: map[string]any{
				"kind": "proof", "open_gates": []string{"fanout_planned"},
				"coordinator_advances": true,
			},
			want: []string{"`fanout_planned`", "has not passed yet", "call `workflow_advance`"},
		},
		{
			name: "proof with dormant gates",
			exit: map[string]any{
				"kind": "proof", "dormant_gates": []string{"obligation_settled:scan"},
			},
			want: []string{"`obligation_settled:scan`", "dormant", "not failing",
				"host advances on its own"},
		},
		{
			name: "proof with complete_when",
			exit: map[string]any{"kind": "proof", "complete_when": "child_run_complete"},
			want: []string{"completes on `child_run_complete`"},
		},
		{
			name: "proof with everything satisfied",
			exit: map[string]any{"kind": "proof", "coordinator_advances": true},
			want: []string{"Every gate for this phase is satisfied"},
		},
		{
			name: "choice transitions split by actor",
			exit: map[string]any{
				"kind": "proof",
				"choice_transitions": []map[string]any{
					{"id": "deepen", "label": "Go deeper", "coordinator_may_fire": true},
					{"id": "approve", "label": "Accept", "actors": []string{"human"},
						"coordinator_may_fire": false},
				},
			},
			want: []string{"Choice transitions out of this phase",
				`workflow_transition(transition_id="deepen")`, "you cannot fire it"},
			// Gate leaves and phase transitions use distinct terms.
			avoid: []string{"Choice leaves"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := renderActiveWorkflow(t, tc.exit)
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("phase exit %q missing %q in:\n%s", tc.name, want, out)
				}
			}
			for _, avoid := range tc.avoid {
				if strings.Contains(out, avoid) {
					t.Errorf("phase exit %q must not contain %q in:\n%s", tc.name, avoid, out)
				}
			}
		})
	}
}
