package definition_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"

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

// Each enum field accepts exactly its declared values; the same manifest with
// a declared value parses, so a rejection is the value and nothing else.
func TestParsePhaseAdvanceEnumsAcceptOnlyDeclaredValues(t *testing.T) {
	manifest := func(block string) []byte {
		return []byte("id: enums\nversion: 1.0.0\nphases:\n  - id: x\n    activity_label: Test phase\n" + block)
	}
	for _, tc := range []struct {
		name  string
		block func(string) string
		valid map[string]func(workflowdef.PhaseDef) bool
	}{
		{
			name:  "advance.when_gate_met",
			block: func(v string) string { return "    advance:\n      when_gate_met: " + v + "\n" },
			valid: map[string]func(workflowdef.PhaseDef) bool{
				"auto": func(p workflowdef.PhaseDef) bool { return p.AdvanceWhenGateMet == workflowdef.AdvanceWhenGateMetAuto },
				"coordinator": func(p workflowdef.PhaseDef) bool {
					return p.AdvanceWhenGateMet == workflowdef.AdvanceWhenGateMetCoordinator
				},
			},
		},
		{
			name:  "loop.exit",
			block: func(v string) string { return "    loop:\n      exit: " + v + "\n" },
			valid: map[string]func(workflowdef.PhaseDef) bool{
				"next": func(p workflowdef.PhaseDef) bool { return p.LoopExit == workflowdef.LoopExitNext },
				"self": func(p workflowdef.PhaseDef) bool { return p.LoopExit == workflowdef.LoopExitSelf },
			},
		},
	} {
		for value, typed := range tc.valid {
			m, err := workflowdef.ParseManifestYAML(manifest(tc.block(value)))
			testutil.FailErr(t, tc.name+" "+value, err)
			phase, ok := m.PhaseByID("x")
			if !ok || !typed(phase) {
				t.Errorf("%s %q: parsed phase = %+v", tc.name, value, phase)
			}
		}
		for _, value := range []string{"never", "host", "Auto"} {
			if _, err := workflowdef.ParseManifestYAML(manifest(tc.block(value))); err == nil {
				t.Errorf("%s %q parsed; want a rejection", tc.name, value)
			}
		}
	}
}

func TestEffectiveAdvancePolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		controls string
		advance  string
		want     workflowdef.AdvanceWhenGateMet
	}{
		{name: "default is the coordinator", want: workflowdef.AdvanceWhenGateMetCoordinator},
		{name: "host phase advance defaults to auto", controls: "host", want: workflowdef.AdvanceWhenGateMetAuto},
		{name: "phase auto wins over the default", advance: "auto", want: workflowdef.AdvanceWhenGateMetAuto},
		{name: "phase coordinator wins over host", controls: "host", advance: "coordinator", want: workflowdef.AdvanceWhenGateMetCoordinator},
		{name: "phase auto under host", controls: "host", advance: "auto", want: workflowdef.AdvanceWhenGateMetAuto},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "id: policy\nversion: 1.0.0\n"
			if tc.controls != "" {
				src += "controls:\n  phase_advance: " + tc.controls + "\n"
			}
			src += "phases:\n  - id: x\n    activity_label: Test phase\n"
			if tc.advance != "" {
				src += "    advance:\n      when_gate_met: " + tc.advance + "\n"
			}
			m, err := workflowdef.ParseManifestYAML([]byte(src))
			testutil.FailErr(t, "ParseManifestYAML", err)
			phase, ok := m.PhaseByID("x")
			if !ok {
				t.Fatal("missing phase x")
			}
			if got := workflowdef.EffectiveAdvancePolicy(m, phase); got != tc.want {
				t.Fatalf("EffectiveAdvancePolicy = %q want %q", got, tc.want)
			}
			if got := workflowdef.PhaseHostPhaseAdvance(m, "x"); got != (tc.want == workflowdef.AdvanceWhenGateMetAuto) {
				t.Fatalf("PhaseHostPhaseAdvance = %v for policy %q", got, tc.want)
			}
		})
	}
}

// A project overlay with an undeclared advance value stops the registry load;
// the same overlay with a declared value loads and carries it.
func TestRegistryLoadRejectsInvalidAdvanceValue(t *testing.T) {
	load := func(value string) (*workflowdef.Registry, error) {
		projectDir := t.TempDir()
		dir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows", "advance-probe")
		testutil.FailErr(t, "create overlay directory", os.MkdirAll(dir, 0o755))
		src := "id: advance-probe\nversion: 1.0.0\nname: probe\ntrigger: /advance-probe\nrequest:\n  question: What?\n" +
			"phases:\n  - id: x\n    activity_label: Probe phase\n    advance:\n      when_gate_met: " + value + "\n"
		testutil.FailErr(t, "write overlay", os.WriteFile(filepath.Join(dir, "workflow.yaml"), []byte(src), 0o644))
		return workflowdef.RegistryFromDirs(projectDir)
	}
	reg, err := load("auto")
	testutil.FailErr(t, "load registry with a valid overlay", err)
	m, err := reg.Get("advance-probe", "1.0.0")
	testutil.FailErr(t, "Get advance-probe", err)
	if phase, _ := m.PhaseByID("x"); phase.AdvanceWhenGateMet != workflowdef.AdvanceWhenGateMetAuto {
		t.Fatalf("overlay advance = %q want auto", phase.AdvanceWhenGateMet)
	}
	if _, err := load("never"); err == nil {
		t.Fatal("registry loaded an overlay with advance.when_gate_met never")
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
