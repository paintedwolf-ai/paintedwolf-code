package contract

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/internal/workflow/verdictcall"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
	"maps"
	"path/filepath"
	"strings"
	"testing"
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

// catalogVerdictCall loads the shipped submit_verdict call schema that review
// phases compose their accepted call from.
func catalogVerdictCall(t *testing.T) map[string]any {
	t.Helper()
	cfg, err := toolschema.LoadSchemaDir(filepath.Join(configlayout.FindModuleRoot(), "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "load tool schemas", err)
	meta, ok := cfg.ToolMeta("submit_verdict")
	if !ok {
		t.Fatal("submit_verdict schema missing")
	}
	return meta.ArgsSchema
}

// assertVerdictProjection holds the prompt outline, the offered call schema,
// and admission to one phase declaration.
func assertVerdictProjection(t *testing.T, manifest workflowdef.Manifest, phase workflowdef.PhaseDef) {
	t.Helper()
	def := *phase.ReviewLoop
	def.VerdictSchema = maps.Clone(def.VerdictSchema)
	phase.ReviewLoop = &def
	catalog := catalogVerdictCall(t)
	project := func() *inject.PhaseExitView {
		view := workflowpresentation.ProjectPhaseExit(manifest, phase, nil, nil).InjectView()
		contractcheck.FailErr(t, "attach verdict call", verdictcall.Attach(view, catalog, *phase.ReviewLoop, manifest.ReportBrief()))
		return view
	}
	exit := project()
	snapshot := inject.WorkflowRuntimeSnapshot{PhaseExit: exit}
	data := inject.BuildActiveWorkflowInjectData(inject.CoordinatorTurnFrame{Runtime: snapshot})
	projectedOutline := func() string {
		return inject.ActiveWorkflowInjectToMap(data, nil, nil)["phase_exit"].(map[string]any)["verdict_outline"].(string)
	}
	raw := projectedOutline()
	offered := tools.TrimCoordinatorToolMeta(tools.ToolMeta{Name: "submit_verdict", ArgsSchema: exit.SubmitVerdictArgsSchema}).ArgsSchema
	if raw != verdictcall.Outline(exit.SubmitVerdictArgsSchema) {
		t.Fatalf("projected outline = %q, want the offered schema's outline", raw)
	}
	schema := phase.ReviewLoop.VerdictSchema
	for key := range schema {
		if !strings.Contains(raw, key) {
			t.Fatalf("projected outline %q omits field %q", raw, key)
		}
	}
	if !strings.Contains(raw, "verdict: "+strings.ReplaceAll(schema["verdict"], " ", "")) {
		t.Fatalf("projected outline %q omits the decision values %q", raw, schema["verdict"])
	}
	if !strings.Contains(renderPhaseExitView(t, exit), raw) {
		t.Fatal("rendered phase exit lost the structured verdict contract")
	}
	for i, value := range strings.Split(schema["verdict"], "|") {
		verdict := map[string]string{"verdict": strings.TrimSpace(value)}
		call := map[string]any{"verdict": strings.TrimSpace(value)}
		for key, kind := range schema {
			if key == "verdict" {
				continue
			}
			verdict[key] = "bounded evidence"
			call[key] = "bounded evidence"
			switch kind {
			case workflowdef.VerdictCoverageType:
				verdict[key] = `{ "revision": "fixture", "assessments": [] }`
				call[key] = map[string]any{"revision": "fixture", "assessments": []any{}}
			case workflowdef.VerdictClaimsType, workflowdef.VerdictSetAsidesType:
				verdict[key] = "[]"
				call[key] = []any{}
			}
		}
		contractcheck.FailErr(t, "admit schema-derived verdict", workflowvalidation.ValidateReviewLoopVerdict(*phase.ReviewLoop, verdict, workflowvalidation.VerdictRules{}))
		contractcheck.FailErr(t, "offered schema accepts the admitted verdict", tools.ValidateToolArgs(offered, map[string]any{"verdict": call}))
		if workflowvalidation.ReviewLoopVerdictTerminal(*phase.ReviewLoop, verdict) != (i == 0) {
			t.Fatal("projected enum order disagrees with terminal admission")
		}
		for key := range schema {
			missing := maps.Clone(verdict)
			delete(missing, key)
			if workflowvalidation.ValidateReviewLoopVerdict(*phase.ReviewLoop, missing, workflowvalidation.VerdictRules{}) == nil {
				t.Fatalf("omitted required field %q accepted", key)
			}
			missingCall := maps.Clone(call)
			delete(missingCall, key)
			if tools.ValidateToolArgs(offered, map[string]any{"verdict": missingCall}) == nil {
				t.Fatalf("offered schema accepts a verdict without %q", key)
			}
		}
	}
	before := projectedOutline()
	phase.ReviewLoop.VerdictSchema["new_required_field"] = "string"
	if exit.VerdictOutline != before {
		t.Fatal("projection aliases manifest schema")
	}
	data.PhaseExit = project()
	if before == projectedOutline() {
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
