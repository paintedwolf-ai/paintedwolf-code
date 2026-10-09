package workflow

import (
	"context"
	"github.com/lycaon/lycaon/internal/spawn"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
)

func TestValidateFanoutPlan(t *testing.T) {
	plan := runstate.FanoutPlan{Legs: []runstate.FanoutPlanLeg{
		{AgentType: "path-explorer", Prompt: "Map src layout"},
		{AgentType: "repo-researcher", Prompt: "Find CI entrypoints"},
	}}
	if err := validateFanoutPlan(plan, []string{"coordinator", "path-explorer", "repo-researcher"}, nil, 4, false); err != nil {
		t.Fatalf("validate: %v", err)
	}
	plan.Legs = append(plan.Legs, runstate.FanoutPlanLeg{AgentType: "path-explorer", Prompt: "extra"})
	if err := validateFanoutPlan(plan, []string{"path-explorer"}, nil, 2, false); err == nil {
		t.Fatal("expected leg cap error")
	}
	if err := validateFanoutPlan(runstate.FanoutPlan{Legs: []runstate.FanoutPlanLeg{
		{AgentType: "implementer", Prompt: "write stuff", Scope: &api.TaskScope{Mode: "write"}},
	}}, []string{"implementer"}, nil, 3, false); err == nil {
		t.Fatal("expected write scope reject")
	}
}

func TestValidateFanoutPlanRejectsReviewLoopReviewers(t *testing.T) {
	plan := runstate.FanoutPlan{Legs: []runstate.FanoutPlanLeg{
		{AgentType: "skeptic", Prompt: "Argue against the user ask"},
	}}
	err := validateFanoutPlan(plan, []string{"skeptic", "security-reviewer"}, []string{"skeptic"}, 5, false)
	if err == nil || !strings.Contains(err.Error(), "review_loop reviewer") {
		t.Fatalf("err = %v want review_loop reviewer reject", err)
	}
}

func TestValidateFanoutPlanRequiresThreatModel(t *testing.T) {
	plan := runstate.FanoutPlan{Legs: []runstate.FanoutPlanLeg{
		{AgentType: "security-reviewer", Prompt: "Map auth entry points"},
	}}
	if err := validateFanoutPlan(plan, []string{"security-reviewer"}, nil, 5, true); err == nil {
		t.Fatal("expected empty threat_model reject")
	}
	plan.ThreatModel = "desktop app; same-user local attacker; OS login is the boundary"
	if err := validateFanoutPlan(plan, []string{"security-reviewer"}, nil, 5, true); err != nil {
		t.Fatalf("validate with threat_model: %v", err)
	}
	if err := validateFanoutPlan(plan, []string{"security-reviewer"}, nil, 5, false); err != nil {
		t.Fatalf("optional threat_model still valid: %v", err)
	}
}

func TestFanoutPlanRoundTrip(t *testing.T) {
	vars := runstate.StampFanoutPlan(nil, runstate.FanoutPlan{
		Phase:       "execute",
		Legs:        []runstate.FanoutPlanLeg{{AgentType: "security-reviewer", Prompt: "Survey auth"}},
		Rationale:   "focused survey",
		ThreatModel: "library; caller process; no network listener",
	})
	plan, ok := runstate.FanoutPlanForPhase(vars, workflowdef.PhaseDef{ID: "execute", Gates: []string{"worker_cycle_ready"}})
	if !ok || len(plan.Legs) != 1 || plan.Legs[0].AgentType != "security-reviewer" {
		t.Fatalf("plan = %+v ok=%v", plan, ok)
	}
	if plan.ThreatModel != "library; caller process; no network listener" {
		t.Fatalf("threat_model = %q", plan.ThreatModel)
	}
}

func TestParseFanoutPlanArgsThreatModel(t *testing.T) {
	plan, err := parseFanoutPlanArgs(map[string]any{
		"threat_model": "CLI; local user; no auth",
		"rationale":    "one surface",
		"legs": []any{
			map[string]any{"agent_type": "security-reviewer", "subject": "Input parsing", "prompt": "Map stdin parsers"},
		},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if plan.ThreatModel != "CLI; local user; no auth" || plan.Rationale != "one surface" {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestFanoutPlanLegCarriesItsCeiling(t *testing.T) {
	plan, err := parseFanoutPlanArgs(map[string]any{
		"legs": []any{
			map[string]any{"agent_type": "security-reviewer", "subject": "Confinement", "prompt": "Trace every spawn site", "max_tool_loops": float64(40)},
			map[string]any{"agent_type": "security-reviewer", "subject": "Egress", "prompt": "Trace the broker"},
		},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if plan.Legs[0].MaxToolLoops != 40 || plan.Legs[1].MaxToolLoops != 0 {
		t.Fatalf("leg ceilings = %d, %d want 40 and the default", plan.Legs[0].MaxToolLoops, plan.Legs[1].MaxToolLoops)
	}
	vars := runstate.StampFanoutPlan(nil, runstate.FanoutPlan{Phase: "execute", Legs: plan.Legs})
	stamped, ok := runstate.FanoutPlanForPhase(vars, workflowdef.PhaseDef{ID: "execute", Gates: []string{"worker_cycle_ready"}})
	if !ok || stamped.Legs[0].MaxToolLoops != 40 {
		t.Fatalf("stamped plan = %+v ok=%v want the ceiling to survive stamping", stamped, ok)
	}
	if got := runstate.FormatFanoutPlan(stamped); !strings.Contains(got, "ceiling: 40 tool rounds") {
		t.Fatalf("formatted plan = %q want the leg ceiling", got)
	}
	budget := spawn.WorkerToolBudget{Default: 20, Min: 2, Max: 30}
	if err := validateFanoutLegBudgets(plan, budget); err == nil || !strings.Contains(err.Error(), "legs[0].max_tool_loops") {
		t.Fatalf("validate = %v want legs[0] above the host maximum rejected", err)
	}
	budget.Max = 120
	if err := validateFanoutLegBudgets(plan, budget); err != nil {
		t.Fatalf("validate = %v want in-range ceilings accepted", err)
	}
}

func TestFormatFanoutPlanLeadsWithThreatModel(t *testing.T) {
	got := runstate.FormatFanoutPlan(runstate.FanoutPlan{
		ThreatModel: "CLI; local user; no auth",
		Legs: []runstate.FanoutPlanLeg{{
			AgentType: "security-reviewer",
			Prompt:    "Map stdin parsers",
			Scope:     &api.TaskScope{Mode: api.TaskScopeModeRead, Paths: []string{"internal/parse"}},
		}},
		Rationale: "one surface",
	})
	if !strings.HasPrefix(got, "Threat model: CLI; local user; no auth") {
		t.Fatalf("plan text = %q", got)
	}
	if !strings.Contains(got, "security-reviewer") || !strings.Contains(got, "Rationale: one surface") {
		t.Fatalf("plan text missing leg or rationale: %q", got)
	}
	if !strings.Contains(got, "focus: internal/parse") {
		t.Fatalf("plan text missing focus label: %q", got)
	}
}

func TestFanoutPlanMaxLegsForPhase(t *testing.T) {
	m := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "x", Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "plan", Next: "execute"},
			{ID: "execute", ParallelTask: &workflowdef.ParallelTask{MaxWorkers: 4}},
		},
	})
	def, _ := m.PhaseByID("plan")
	if got := FanoutPlanMaxLegsForPhase(m, def); got != 4 {
		t.Fatalf("max legs = %d want 4", got)
	}
}

func TestWorkflowRuntimeSnapshotProjectsPlanOnlyForWorkerPhase(t *testing.T) {
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "recon", Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "plan", Gates: []string{"fanout_planned"}, Next: "execute"},
			{ID: "execute", Gates: []string{"worker_cycle_ready"}},
		},
	})
	vars := runstate.StampFanoutPlan(nil, runstate.FanoutPlan{
		Phase: "execute",
		Legs:  []runstate.FanoutPlanLeg{{AgentType: "path-explorer", Prompt: "Map the runtime"}},
	})
	snapshots := &workflowruntime.Snapshots{}
	run := &api.WorkflowRun{CurrentPhase: "execute"}
	snap := snapshots.Project(context.Background(), run, manifest, vars)
	if !strings.Contains(snap.FanoutPlan, "Map the runtime") {
		t.Fatalf("execute fanout plan = %q", snap.FanoutPlan)
	}
	vars = runstate.SetHostVar(vars, "fanout_coverage", []runstate.FanoutLegCoverage{{ID: "leg-1", Status: "partial"}})
	snap = snapshots.Project(context.Background(), run, manifest, vars)
	if !strings.Contains(snap.FanoutPlan, `"status":"partial"`) {
		t.Fatalf("execute snapshot omitted recorded coverage: %q", snap.FanoutPlan)
	}

	run.CurrentPhase = "plan"
	snap = snapshots.Project(context.Background(), run, manifest, vars)
	if snap.FanoutPlan != "" {
		t.Fatalf("plan phase should not dispatch stamped fanout: %q", snap.FanoutPlan)
	}
}

func TestFanoutPlanForPhaseDoesNotLeakIntoLaterPhases(t *testing.T) {
	vars := runstate.StampFanoutPlan(nil, runstate.FanoutPlan{
		Phase: "execute",
		Legs:  []runstate.FanoutPlanLeg{{AgentType: "path-explorer", Prompt: "Map the runtime"}},
	})
	if _, ok := runstate.FanoutPlanForPhase(vars, workflowdef.PhaseDef{ID: "execute", Gates: []string{"worker_cycle_ready"}}); !ok {
		t.Fatal("execute phase did not receive its stamped fanout")
	}
	if plan, ok := runstate.FanoutPlanForPhase(vars, workflowdef.PhaseDef{ID: "review", Gates: []string{"evidence_passed:review"}}); ok {
		t.Fatalf("review phase received stale fanout: %+v", plan)
	}
	if _, ok := runstate.FanoutPlanForPhase(vars, workflowdef.PhaseDef{ID: "drill", Gates: []string{"worker_cycle_ready"}}); ok {
		t.Fatal("later wave received another phase's plan")
	}
	vars = runstate.SetHostVar(vars, "fanout_coverage", []runstate.FanoutLegCoverage{{ID: "leg-1", Status: "complete", Settled: true}})
	vars = runstate.SetHostVar(vars, "fanout_settled", true)
	vars = runstate.StampFanoutPlan(vars, runstate.FanoutPlan{Phase: "drill", Legs: []runstate.FanoutPlanLeg{{ID: "leg-1", AgentType: "path-explorer", Prompt: "Drill"}}})
	if _, ok := vars["fanout_coverage"]; ok {
		t.Fatal("new wave retained previous wave's active coverage")
	}
	if settled, _ := vars["fanout_settled"].(bool); settled {
		t.Fatal("new wave inherited previous wave's settled state")
	}
	execute, _ := runstate.FanoutPlanForPhase(vars, workflowdef.PhaseDef{ID: "execute", Gates: []string{"worker_cycle_ready"}})
	drill, _ := runstate.FanoutPlanForPhase(vars, workflowdef.PhaseDef{ID: "drill", Gates: []string{"worker_cycle_ready"}})
	if execute.Legs[0].Prompt != "Map the runtime" || drill.Legs[0].Prompt != "Drill" {
		t.Fatal("later planning erased earlier coverage provenance")
	}
}
