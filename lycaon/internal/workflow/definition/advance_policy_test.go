package definition_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParsePhaseAdvanceAndLoopYAML(t *testing.T) {
	m, err := workflowdef.ParseManifestYAML([]byte(`
id: loop-test
version: 1.0.0
phases:
  - id: research
    activity_label: Test phase
    advance:
      when_gate_met: coordinator
    loop:
      exit: next
    gates: [research_satisfied]
  - id: work
    activity_label: Test phase
    loop:
      exit: self
    gates: [worker_cycle_ready]
    next: work
`))
	testutil.FailErr(t, "ParseManifestYAML", err)
	research, ok := m.PhaseByID("research")
	if !ok {
		t.Fatal("missing research")
	}
	if research.AdvanceWhenGateMet != workflowdef.AdvanceWhenGateMetCoordinator {
		t.Fatalf("advance = %q", research.AdvanceWhenGateMet)
	}
	if research.LoopExit != workflowdef.LoopExitNext {
		t.Fatalf("loop exit = %q", research.LoopExit)
	}
	work, ok := m.PhaseByID("work")
	if !ok {
		t.Fatal("missing work")
	}
	if work.LoopExit != workflowdef.LoopExitSelf {
		t.Fatalf("work loop exit = %q", work.LoopExit)
	}
}
func TestParsePhaseAdvanceRejectsNever(t *testing.T) {
	_, err := workflowdef.ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
phases:
  - id: x
    activity_label: Test phase
    advance:
      when_gate_met: never
`))
	if err == nil {
		t.Fatal("expected error for never")
	}
}
func TestEffectiveAdvancePolicyShippedManifestParity(t *testing.T) {
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)

	hostWorkflows := map[string]struct{}{
		"implement@1.0.0":       {},
		"plan@1.0.0":            {},
		"recon-pack@1.0.0":      {},
		"security-survey@2.0.0": {},
		"security-survey@1.0.0": {},
		"options@1.0.0":         {},
		"bugbash@1.1.0":         {},
		"bugbash@1.0.0":         {},
	}

	for key, m := range reg.All() {
		want := workflowdef.AdvanceWhenGateMetCoordinator
		if _, ok := hostWorkflows[key]; ok {
			want = workflowdef.AdvanceWhenGateMetAuto
		}
		for _, phase := range m.PhaseDefs {
			if phase.AdvanceWhenGateMet != "" {
				if m.ID == "plan" && (phase.ID == "research" || phase.ID == "intake") && phase.AdvanceWhenGateMet != workflowdef.AdvanceWhenGateMetCoordinator {
					t.Fatalf("plan %s advance = %q want coordinator", phase.ID, phase.AdvanceWhenGateMet)
				}
				continue // other explicit overrides validated in manifest YAML tests
			}
			got := workflowdef.EffectiveAdvancePolicy(m, phase)
			if got != want {
				t.Fatalf("%s phase %q: effective policy = %q want %q", key, phase.ID, got, want)
			}
		}
	}
}
func TestResolveAdvanceTargetLoopExitSelf(t *testing.T) {
	m := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "self-loop",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "work",
			CompleteWhen: workflowdef.CompleteWhenGatesSatisfied,
			Gates:        []string{"worker_cycle_ready"},
			Next:         "done",
			LoopExit:     workflowdef.LoopExitSelf,
		}, {ID: "done"}},
	})
	next, ok := m.ResolveAdvanceTarget("work")
	if !ok || next != "work" {
		t.Fatalf("next = %q ok=%v", next, ok)
	}
}
func TestImplementWorkPhaseAdvanceTargetParity(t *testing.T) {
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	m, err := reg.Get("implement", "1.0.0")
	testutil.FailErr(t, "Get implement", err)
	work, ok := m.PhaseByID("work")
	if !ok {
		t.Fatal("missing work phase")
	}
	next, ok := m.ResolveAdvanceTarget("work")
	if !ok || next != "work" {
		t.Fatalf("work→work via next pointer: next=%q ok=%v", next, ok)
	}
	parentID := "parent"
	next, ok = m.ResolveAdvanceTargetForRun(&api.WorkflowRun{ParentRunID: &parentID}, "work")
	if !ok || next != "done" {
		t.Fatalf("child work→done via child_next: next=%q ok=%v", next, ok)
	}
	if workflowdef.EffectiveLoopExit(work) != workflowdef.LoopExitNext {
		t.Fatalf("shipped work loop.exit = %q want default next", work.LoopExit)
	}
	rootDef, ok := m.PhaseForRun(&api.WorkflowRun{}, "work")
	if !ok || rootDef.CompleteWhen != workflowdef.CompleteWhenGatesSatisfied || len(rootDef.Gates) != 1 || rootDef.Gates[0] != "worker_cycle_ready" {
		t.Fatalf("root work contract = complete_when %q gates %v", rootDef.CompleteWhen, rootDef.Gates)
	}
	childDef, ok := m.PhaseForRun(&api.WorkflowRun{ParentRunID: &parentID}, "work")
	if !ok || childDef.CompleteWhen != "delivery_gates_passed" || len(childDef.Gates) != 0 {
		t.Fatalf("child work contract = complete_when %q gates %v", childDef.CompleteWhen, childDef.Gates)
	}
}
