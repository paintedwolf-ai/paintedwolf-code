package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance/feedback"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestManifestAdvancePolicyEnumsParseAndRejectNever(t *testing.T) {
	t.Parallel()
	_, err := workflowdef.ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
phases:
  - id: x
    activity_label: Doing x
    advance:
      when_gate_met: never
`))
	if err == nil || !strings.Contains(err.Error(), "when_gate_met") {
		t.Fatalf("err = %v", err)
	}
	m, err := workflowdef.ParseManifestYAML([]byte(`
id: ok
version: 1.0.0
phases:
  - id: research
    activity_label: Researching
    advance:
      when_gate_met: coordinator
    loop:
      exit: self
`))
	contractcheck.FailErr(t, "ParseManifestYAML", err)
	phase, ok := m.PhaseByID("research")
	if !ok {
		t.Fatal("missing research")
	}
	if phase.AdvanceWhenGateMet != workflowdef.AdvanceWhenGateMetCoordinator {
		t.Fatalf("advance = %q", phase.AdvanceWhenGateMet)
	}
	if phase.LoopExit != workflowdef.LoopExitSelf {
		t.Fatalf("loop.exit = %q", phase.LoopExit)
	}
}

func TestEffectiveAdvancePolicyShippedManifestContractParity(t *testing.T) {
	t.Parallel()
	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "RegistryFromDirs", err)

	hostKeys := map[string]struct{}{
		"implement@1.0.0":       {},
		"plan@1.0.0":            {},
		"recon-pack@1.0.0":      {},
		"security-survey@2.0.0": {},
		"security-survey@1.0.0": {},
		"options@1.0.0":         {},
		// bugbash host-advances so a human triage approval never sits waiting
		// for the coordinator to call workflow_advance (see its manifest note).
		"bugbash@1.1.0": {},
		"bugbash@1.0.0": {},
	}
	coordKeys := map[string]struct{}{}

	for key, m := range reg.All() {
		for _, phase := range m.PhaseDefs {
			if phase.AdvanceWhenGateMet != "" {
				if phase.AdvanceWhenGateMet != workflowdef.AdvanceWhenGateMetAuto &&
					phase.AdvanceWhenGateMet != workflowdef.AdvanceWhenGateMetCoordinator {
					t.Fatalf("%s phase %q: bad advance %q", key, phase.ID, phase.AdvanceWhenGateMet)
				}
				if key == "plan@1.0.0" && phase.ID == "research" &&
					phase.AdvanceWhenGateMet != workflowdef.AdvanceWhenGateMetCoordinator {
					t.Fatalf("plan research advance = %q want coordinator", phase.AdvanceWhenGateMet)
				}
				continue
			}
			got := workflowdef.EffectiveAdvancePolicy(m, phase)
			if _, ok := hostKeys[key]; ok {
				if got != workflowdef.AdvanceWhenGateMetAuto {
					t.Fatalf("%s phase %q: effective = %q want auto", key, phase.ID, got)
				}
			}
			if _, ok := coordKeys[key]; ok {
				if got != workflowdef.AdvanceWhenGateMetCoordinator {
					t.Fatalf("%s phase %q: effective = %q want coordinator", key, phase.ID, got)
				}
			}
		}
	}
}

func TestGateFeedbackSatisfySplitByAdvancePolicy(t *testing.T) {
	t.Parallel()
	catalog, err := feedback.LoadGateFeedbackCatalog()
	contractcheck.FailErr(t, "LoadGateFeedbackCatalog", err)

	coordCtx := feedback.GateFeedbackContext(feedback.WorkflowEvaluationContext{
		AdvanceWhenGateMet: "coordinator",
	}, nil)
	out, err := catalog.RenderGateFeedback(t.Context(), "research_satisfied", coordCtx)
	contractcheck.FailErr(t, "RenderGateFeedback coordinator", err)
	if !strings.Contains(out, "repo-researcher") {
		t.Fatalf("coordinator satisfy missing research dispatch:\n%s", out)
	}
	if strings.Contains(out, "Call workflow_advance") || strings.Contains(out, "workflow_advance") {
		t.Fatalf("gate-feedback must not teach leave via workflow_advance:\n%s", out)
	}

	autoCtx := feedback.GateFeedbackContext(feedback.WorkflowEvaluationContext{
		AdvanceWhenGateMet: "auto",
	}, nil)
	out, err = catalog.RenderGateFeedback(t.Context(), "plan_stub_valid", autoCtx)
	contractcheck.FailErr(t, "RenderGateFeedback auto", err)
	if !strings.Contains(out, "## Approach") {
		t.Fatalf("auto satisfy missing stub sections:\n%s", out)
	}
	if strings.Contains(out, "workflow_advance") {
		t.Fatalf("auto satisfy must not teach workflow_advance:\n%s", out)
	}
}

func TestPhaseExitProjectorPlanContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "RegistryFromDirs", err)
	m, err := reg.Get("plan", "1.0.0")
	contractcheck.FailErr(t, "Get plan", err)

	cases := []struct {
		phase string
		kind  string
		auth  workflowdef.AdvanceWhenGateMet
	}{
		{"research", workflowpresentation.PhaseExitKindProof, workflowdef.AdvanceWhenGateMetCoordinator},
		{"expand", workflowpresentation.PhaseExitKindProof, workflowdef.AdvanceWhenGateMetAuto},
		{"approve", workflowpresentation.PhaseExitKindHumanApproval, workflowdef.AdvanceWhenGateMetAuto},
	}
	for _, tc := range cases {
		def, ok := m.PhaseByID(tc.phase)
		if !ok {
			t.Fatalf("missing phase %q", tc.phase)
		}
		exit := workflowpresentation.ProjectPhaseExit(m, def, nil, nil)
		if exit.Kind != tc.kind {
			t.Fatalf("%s kind = %q want %q", tc.phase, exit.Kind, tc.kind)
		}
		if exit.AdvanceAuthority != string(tc.auth) {
			t.Fatalf("%s auth = %q want %q", tc.phase, exit.AdvanceAuthority, tc.auth)
		}
	}

	injectPath := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "guidance", "active-workflow.md")
	raw, err := os.ReadFile(injectPath)
	contractcheck.FailErr(t, "read active-workflow", err)
	body := string(raw)
	if !strings.Contains(body, "### Phase exit") {
		t.Fatal("active-workflow.md must render Phase exit")
	}
	if strings.Contains(body, "Advance: call") || strings.Contains(body, "Advance: host auto-advances") {
		t.Fatal("active-workflow.md must not use standalone Advance one-liner")
	}

	forbid := []string{"workflow_advance", "host auto-advances", "do **not** call"}
	modePrompts := []struct {
		pack, file string
	}{
		{"hitl", "coordinator-mode-await-user-input.md"},
		{"plan", "coordinator-mode-plan-research.md"},
		{"plan", "coordinator-mode-plan-stub.md"},
	}
	for _, s := range modePrompts {
		raw, err := os.ReadFile(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", s.pack, "agents", "prompts", s.file))
		contractcheck.FailErr(t, "read "+s.file, err)
		body := string(raw)
		for _, bad := range forbid {
			if strings.Contains(body, bad) {
				t.Fatalf("%s must not contain %q", s.file, bad)
			}
		}
	}
}
