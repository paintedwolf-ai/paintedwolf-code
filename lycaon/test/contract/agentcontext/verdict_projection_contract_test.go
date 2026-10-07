package contract

import (
	"maps"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

// Submissions are generated from the projected schemas, so every projected
// field is covered without a second field list.
func TestVerdictProjectionMatchesCatalogAdmission(t *testing.T) {
	manifests, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "load workflow catalog", err)
	checked := 0
	for _, manifest := range manifests {
		for _, phase := range manifest.PhaseDefs {
			if phase.ReviewLoop == nil {
				continue
			}
			checked++
			t.Run(manifest.ID+"/"+phase.ID, func(t *testing.T) {
				assertVerdictProjection(t, manifest, phase)
				def := *phase.ReviewLoop
				def.VerdictSchema = maps.Clone(def.VerdictSchema)
				def.VerdictSchema["verdict"] = "SETTLED|REWORK"
				def.VerdictSchema["additional_context"] = "string"
				def.VerdictSchema["additional_claims"] = "claims"
				def.VerdictSchema["additional_set_asides"] = "set_asides"
				phase.ReviewLoop = &def
				assertVerdictProjection(t, manifest, phase)
			})
		}
	}
	if checked == 0 {
		t.Fatal("no review loops exercised")
	}
}

func assertVerdictProjection(t *testing.T, manifest workflowdef.Manifest, phase workflowdef.PhaseDef) {
	t.Helper()
	def := *phase.ReviewLoop
	def.VerdictSchema = maps.Clone(def.VerdictSchema)
	phase.ReviewLoop = &def
	exit := workflow.ProjectPhaseExit(manifest, phase, nil, nil)
	snapshot := inject.WorkflowRuntimeSnapshot{PhaseExit: exit.InjectView()}
	data := inject.BuildActiveWorkflowInjectData(inject.CoordinatorTurnFrame{Runtime: snapshot})
	vars := inject.ActiveWorkflowInjectToMap(data, nil, nil)
	raw := vars["phase_exit"].(map[string]any)["verdict_shape"].(string)
	schema := phase.ReviewLoop.VerdictSchema
	if raw != workflow.VerdictSchemaShape(*phase.ReviewLoop) {
		t.Fatalf("projected shape = %q, want the admission shape", raw)
	}
	for key, kind := range schema {
		if !strings.Contains(raw, key+": ") {
			t.Fatalf("projected shape %q omits field %q", raw, key)
		}
		if kind == workflowdef.VerdictClaimsType {
			for _, word := range phase.ReviewLoop.StatusWords() {
				if !strings.Contains(raw, word) {
					t.Fatalf("projected shape %q omits claim status %q", raw, word)
				}
			}
		}
	}
	if !strings.Contains(renderPhaseExitBlock(t, exit), raw) {
		t.Fatal("rendered phase exit lost the structured verdict contract")
	}
	for i, value := range strings.Split(schema["verdict"], "|") {
		verdict := map[string]string{"verdict": strings.TrimSpace(value)}
		for key, kind := range schema {
			if key == "verdict" {
				continue
			}
			verdict[key] = "bounded evidence"
			if kind == workflowdef.VerdictCoverageType {
				verdict[key] = `{ "revision": "fixture", "assessments": [] }`
			}
			if kind == workflowdef.VerdictClaimsType || kind == workflowdef.VerdictSetAsidesType {
				verdict[key] = "[]"
			}
		}
		contractcheck.FailErr(t, "admit schema-derived verdict", workflow.ValidateReviewLoopVerdict(*phase.ReviewLoop, verdict, workflow.VerdictRules{}))
		if workflow.ReviewLoopVerdictTerminal(*phase.ReviewLoop, verdict) != (i == 0) {
			t.Fatal("projected enum order disagrees with terminal admission")
		}
		for key := range schema {
			missing := maps.Clone(verdict)
			delete(missing, key)
			if workflow.ValidateReviewLoopVerdict(*phase.ReviewLoop, missing, workflow.VerdictRules{}) == nil {
				t.Fatalf("omitted required field %q accepted", key)
			}
		}
	}
	projectedShape := func() string {
		return inject.ActiveWorkflowInjectToMap(data, nil, nil)["phase_exit"].(map[string]any)["verdict_shape"].(string)
	}
	before := projectedShape()
	phase.ReviewLoop.VerdictSchema["new_required_field"] = "string"
	if exit.VerdictShape != before {
		t.Fatal("projection aliases manifest schema")
	}
	data.PhaseExit = workflow.ProjectPhaseExit(manifest, phase, nil, nil).InjectView()
	if before == projectedShape() {
		t.Fatal("schema change did not reach the prompt projection")
	}
}

func TestComposeSurfaceCanStartItsPersistedWorkflow(t *testing.T) {
	plan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: "workflow_compose"}, 1)
	contractcheck.FailErr(t, "compile compose tool plan", err)
	for _, action := range []string{"workflow_compose", "workflow_persist", "state_start"} {
		if !contractcheck.ContainsString(plan.ImmediateNames(), action) {
			t.Errorf("compose lifecycle action %q unavailable", action)
		}
	}
}
