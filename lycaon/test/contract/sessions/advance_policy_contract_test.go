package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolvocab"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const workflowAdvanceTool = "workflow_advance"

// Gate feedback renders under both advance policies, and every tool it names
// is one the phase's coordinator surface offers under that policy: the host
// refuses workflow_advance on an auto phase and promotes it on a coordinator one.
func TestGateFeedbackSatisfySplitByAdvancePolicy(t *testing.T) {
	t.Parallel()
	catalog, err := feedback.LoadGateFeedbackCatalog()
	contractcheck.FailErr(t, "LoadGateFeedbackCatalog", err)
	vocab := gateFeedbackToolCatalog(t)
	policies := []workflowdef.AdvanceWhenGateMet{workflowdef.AdvanceWhenGateMetAuto, workflowdef.AdvanceWhenGateMetCoordinator}

	if len(catalog.GateIDs()) == 0 {
		t.Fatal("no gate feedback keys loaded")
	}
	for _, gateID := range catalog.GateIDs() {
		for _, policy := range policies {
			out := renderGateFeedback(t, catalog, gateID, feedback.WorkflowEvaluationContext{AdvanceWhenGateMet: string(policy)})
			if out == "" {
				t.Errorf("%s under %s renders nothing", gateID, policy)
			}
		}
	}

	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "RegistryFromDirs", err)
	checked := 0
	for key, m := range reg.All() {
		for _, phase := range m.PhaseDefs {
			for _, gateID := range phase.Gates {
				if !catalog.Has(gateID) {
					continue
				}
				for _, policy := range policies {
					offered := offeredCoordinatorTools(t, api.CoordinatorRunContext{
						WorkflowID: m.ID, WorkflowVersion: m.Version, RunStatus: "running",
						CurrentPhase: phase.ID, FailedLeaves: []string{gateID},
						PhaseCoordinatorSurface: phase.CoordinatorSurface, PhaseSurfaceTemplate: phase.SurfaceTemplate,
						AdvanceWhenGateMet: string(policy),
					})
					out := renderGateFeedback(t, catalog, gateID, feedback.WorkflowEvaluationContext{
						WorkflowID: m.ID, CurrentPhase: phase.ID, FailedLeaves: []string{gateID},
						AdvanceWhenGateMet: string(policy), RunActive: true,
					})
					for _, name := range toolvocab.ToolsIn(vocab, out) {
						if !offered(name) {
							t.Errorf("%s phase %s gate %s under %s names %s, which that surface does not offer",
								key, phase.ID, gateID, policy, name)
						}
					}
					checked++
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no shipped phase binds a gate with feedback")
	}
}

func renderGateFeedback(t *testing.T, catalog *feedback.GateFeedbackCatalog, gateID string, wf feedback.WorkflowEvaluationContext) string {
	t.Helper()
	out, err := catalog.RenderGateFeedback(t.Context(), gateID, feedback.GateFeedbackContext(wf, nil))
	contractcheck.FailErr(t, "RenderGateFeedback "+gateID, err)
	return out
}

// offeredCoordinatorTools resolves the turn's surface as the prompt loop does and
// applies the advance policy's effect on workflow_advance.
func offeredCoordinatorTools(t *testing.T, runCtx api.CoordinatorRunContext) func(string) bool {
	t.Helper()
	profile := surface.ResolveTurnProfile(runCtx, &api.Session{Posture: api.SessionPostureBuild}, nil)
	plan, err := surface.CompileToolPlan(profile, 1)
	contractcheck.FailErr(t, "CompileToolPlan "+profile.SurfaceID, err)
	coordinator := runCtx.AdvanceWhenGateMet == string(workflowdef.AdvanceWhenGateMetCoordinator)
	if coordinator {
		plan = plan.Promote(workflowAdvanceTool)
	}
	return func(name string) bool {
		if name == workflowAdvanceTool && !coordinator {
			return false
		}
		return plan.Addressable(name)
	}
}

func gateFeedbackToolCatalog(t *testing.T) *toolvocab.Catalog {
	t.Helper()
	eff := contractcheck.StockCatalog(t)
	profiles, err := sandbox.LoadToolProfilesWithCatalog(eff)
	contractcheck.FailErr(t, "load tool profiles", err)
	schemas, _, err := extpacks.LoadEffectiveToolSchemas(eff)
	contractcheck.FailErr(t, "load tool schemas", err)
	vocab, err := toolvocab.NewCatalog(schemas, profiles, toolvocab.Provenance{})
	contractcheck.FailErr(t, "toolvocab.NewCatalog", err)
	return vocab
}

func TestPhaseExitProjectorPlanContract(t *testing.T) {
	t.Parallel()
	reg, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "RegistryFromDirs", err)
	m, err := reg.Get("plan", "1.0.1")
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

}
