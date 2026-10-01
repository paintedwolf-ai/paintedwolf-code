package workflow

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestParseManifestYAMLCloseoutGated(t *testing.T) {
	m, err := workflowdef.ParseManifestYAML([]byte(`
id: gated
version: 1.0.0
phases:
  - id: execute
    activity_label: Executing
    complete_when: gates_satisfied
    gates: [worker_cycle_ready]
    controls:
      closeout: gated
    next: done
  - id: done
    activity_label: Done
    terminal: true
    complete_when: orchestration_complete
`))
	testutil.FailErr(t, "ParseManifestYAML failed", err)
	execute, ok := m.PhaseByID("execute")
	if !ok || !execute.Closeout.Gated() {
		t.Fatalf("execute.Closeout = %q want gated", execute.Closeout)
	}
	done, ok := m.PhaseByID("done")
	if !ok || done.Closeout.Gated() {
		t.Fatalf("done.Closeout = %q want free", done.Closeout)
	}
}

func TestParseManifestYAMLRejectsInvalidCloseout(t *testing.T) {
	_, err := workflowdef.ParseManifestYAML([]byte(`
id: gated
version: 1.0.0
phases:
  - id: execute
    activity_label: Executing
    complete_when: gates_satisfied
    gates: [worker_cycle_ready]
    controls:
      closeout: strict
`))
	if err == nil || !strings.Contains(err.Error(), "invalid controls.closeout") {
		t.Fatalf("err = %v, want invalid controls.closeout", err)
	}
}

func TestParseManifestYAMLCloseoutGatedShapeRules(t *testing.T) {
	cases := map[string]struct {
		phase string
		want  string
	}{
		"terminal": {
			phase: `
  - id: done
    activity_label: Done
    terminal: true
    complete_when: orchestration_complete
    controls:
      closeout: gated
`,
			want: "invalid on a terminal phase",
		},
		"no complete_when": {
			phase: `
  - id: chat
    activity_label: Chatting
    controls:
      closeout: gated
`,
			want: "requires complete_when",
		},
		"human approval": {
			phase: `
  - id: approve
    activity_label: Approving
    human_approval: {}
    complete_when: gates_satisfied
    gates: [human_approval]
    controls:
      closeout: gated
`,
			want: "invalid with human_approval",
		},
		"review loop": {
			phase: `
  - id: review
    activity_label: Reviewing
    review_loop:
      evidence_key: fixture
      iteration_cap: 1
      verdict_schema:
        verdict: APPROVED
    complete_when: gates_satisfied
    gates: ["evidence_passed:fixture"]
    controls:
      closeout: gated
`,
			want: "invalid with review_loop",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := workflowdef.ParseManifestYAML([]byte("id: gated\nversion: 1.0.0\nphases:" + tc.phase))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMergePhaseDefCloseoutOverride(t *testing.T) {
	parent := workflowdef.PhaseDef{ID: "work", CompleteWhen: workflowdef.CompleteWhenGatesSatisfied, Gates: []string{"worker_cycle_ready"}}
	child := workflowdef.PhaseDef{ID: "work", Closeout: workflowdef.CloseoutGated}
	merged := workflowdef.MergePhaseDef(parent, child)
	if !merged.Closeout.Gated() {
		t.Fatalf("merged.Closeout = %q want gated", merged.Closeout)
	}
	unchanged := workflowdef.MergePhaseDef(parent, workflowdef.PhaseDef{ID: "work"})
	if unchanged.Closeout.Gated() {
		t.Fatalf("unchanged.Closeout = %q want free", unchanged.Closeout)
	}
}

func TestValidateGatedCloseoutAsk(t *testing.T) {
	gated := workflowdef.PhaseDef{ID: "execute", Closeout: workflowdef.CloseoutGated}
	if diags := ValidateGatedCloseoutAsk(gated, "survey_execute", []string{"task", "ask_user"}); len(diags) != 0 {
		t.Fatalf("surface offering ask_user must validate clean, got %v", diags)
	}
	diags := ValidateGatedCloseoutAsk(gated, "survey_execute", []string{"task"})
	if len(diags) != 1 || diags[0].Code != "missing_gated_closeout_ask" {
		t.Fatalf("diags = %+v want missing_gated_closeout_ask", diags)
	}
	// A phase without the control never needs the channel.
	free := workflowdef.PhaseDef{ID: "execute"}
	if diags := ValidateGatedCloseoutAsk(free, "survey_execute", []string{"task"}); len(diags) != 0 {
		t.Fatalf("ungated phase must not require ask_user, got %v", diags)
	}
	// Unresolved surface binding defers to turn time.
	if diags := ValidateGatedCloseoutAsk(gated, "", nil); len(diags) != 0 {
		t.Fatalf("empty surface must defer, got %v", diags)
	}
}

// TestAmbientImplementCloseoutStaysFree keeps ambient chat ungated.
func TestAmbientImplementCloseoutStaysFree(t *testing.T) {
	path := filepath.Join("..", "..", "config", "packs", "painted-wolf", "implement", "workflows", "implement", "workflow.yaml")
	m, err := workflowdef.LoadManifestFromFile(path)
	testutil.FailErr(t, "load implement manifest", err)
	for _, p := range m.PhaseDefs {
		if p.Closeout.Gated() {
			t.Fatalf("ambient implement phase %q declares a gated closeout; ambient chat must close freely", p.ID)
		}
	}
}

// TestBundledGatedCloseoutAdoption pins which bundled phases run to completion.
func TestBundledGatedCloseoutAdoption(t *testing.T) {
	want := map[string][]string{
		"bugbash":         {"hunt", "triage", "expand", "closeout"},
		"security-survey": {"plan", "execute"},
		"recon-pack":      {"plan", "execute", "reconcile", "drill_plan", "drill"},
		"plan":            {"research", "expand"},
		"options":         {"fan_out"},
	}
	for id, phases := range want {
		path := filepath.Join("..", "..", "config", "packs", "painted-wolf", id, "workflows", id, "workflow.yaml")
		m, err := workflowdef.LoadManifestFromFile(path)
		testutil.FailErr(t, "load "+id+" manifest", err)
		gated := map[string]bool{}
		for _, p := range m.PhaseDefs {
			if p.Closeout.Gated() {
				gated[p.ID] = true
			}
		}
		for _, phase := range phases {
			if !gated[phase] {
				t.Errorf("%s phase %q should declare controls.closeout: gated", id, phase)
			}
			delete(gated, phase)
		}
		for phase := range gated {
			t.Errorf("%s phase %q declares an unexpected gated closeout", id, phase)
		}
	}
}
