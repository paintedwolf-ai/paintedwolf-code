package workerplan_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerplan"
)

const featurePlan = ".lycaon/plans/feature.md"

const featureTasks = `[
	{"id":"leg-1","title":"Schema","files":["internal/db/a.go"]},
	{"id":"leg-2","title":"Service","files":["internal/svc/b.go"],"depends_on":["leg-1"]},
	{"id":"leg-3","title":"Extra","files":["internal/extra/c.go"]}
]`

func TestPlanningIsRefusedUntilTheBlueprintIsApprovedAndReady(t *testing.T) {
	env := newPlanEnv(t)
	env.blueprintWith(featurePlan, featureTasks)

	_, err := env.plan(t, "missing", ".lycaon/plans/absent.md")
	wantReject(t, err, "blueprint_not_found")
	_, err = env.plan(t, "no-path", " ")
	wantReject(t, err, "blueprint_path_required")

	env.approvals.approved = false
	_, err = env.plan(t, "unapproved", featurePlan)
	wantReject(t, err, "plan_not_approved")

	env.approvals.approved, env.ready.ready = true, false
	_, err = env.plan(t, "unready", featurePlan)
	wantReject(t, err, "implement_workflow_not_ready")

	workers, err := env.records.Workers(t.Context(), env.coord)
	testutil.FailErr(t, "workers", err)
	if len(workers) != 0 {
		t.Fatalf("refused planning created %d workers", len(workers))
	}
}

func TestPlanningCreatesOneWorkerPerTaskWithItsPrerequisites(t *testing.T) {
	env := newPlanEnv(t)
	env.blueprintWith(featurePlan, featureTasks)

	seeded, err := env.plan(t, "op-1", featurePlan)
	testutil.FailErr(t, "plan", err)
	if seeded.BaseSHA != "abc1234" || len(seeded.Legs) != 3 {
		t.Fatalf("seeded = %+v", seeded)
	}
	for i, want := range []struct {
		work  string
		after []string
	}{{"leg-1", nil}, {"leg-2", []string{"leg-1"}}, {"leg-3", nil}} {
		leg := seeded.Legs[i]
		if leg.WorkID != want.work || leg.Worker != want.work || leg.State != "planned" || !slices.Equal(leg.AfterWorkers, want.after) {
			t.Fatalf("leg %d = %+v, want %s after %v", i, leg, want.work, want.after)
		}
	}

	again, err := env.plan(t, "op-2", featurePlan)
	testutil.FailErr(t, "plan again", err)
	workers, err := env.records.Workers(t.Context(), env.coord)
	testutil.FailErr(t, "workers", err)
	if len(workers) != 3 || again.BlueprintRevision != seeded.BlueprintRevision || len(again.Withdrawn) != 0 {
		t.Fatalf("planning twice left %d workers, result %+v", len(workers), again)
	}
}

func TestRevisedBlueprintRestagesUnstartedWorkersAndLeavesStartedOnes(t *testing.T) {
	env := newPlanEnv(t)
	env.blueprintWith(featurePlan, featureTasks)
	_, err := env.plan(t, "op-1", featurePlan)
	testutil.FailErr(t, "plan", err)

	planned, err := env.records.ByBlueprint(t.Context(), env.coord, featurePlan)
	testutil.FailErr(t, "by blueprint", err)
	for _, w := range planned {
		if w.WorkflowWorkID == "leg-1" {
			env.startRound(t, w.ID, "job-leg-1")
			env.settleRound(t, "job-leg-1")
		}
	}

	env.blueprintWith(featurePlan, `[
		{"id":"leg-1","title":"Schema, edited after it ran","files":["internal/db/other.go"]},
		{"id":"leg-2","title":"Service","files":["internal/svc/b2.go"],"depends_on":["leg-1"]}
	]`)
	revised, err := env.plan(t, "op-2", featurePlan)
	testutil.FailErr(t, "revise", err)

	if !slices.Equal(revised.Withdrawn, []string{"leg-3"}) {
		t.Fatalf("withdrawn = %v, want the dropped task's worker", revised.Withdrawn)
	}
	if leg := revised.Legs[0]; leg.State != "completed" || !slices.Equal(leg.Files, []string{"internal/db/a.go"}) {
		t.Fatalf("a worker that ran was restaged: %+v", leg)
	}
	if leg := revised.Legs[1]; leg.State != "planned" || !slices.Equal(leg.Files, []string{"internal/svc/b2.go"}) {
		t.Fatalf("an unstarted worker kept its old task: %+v", leg)
	}

	names, err := env.svc.PlanWorkers(t.Context(), env.coord.SessionID, featurePlan)
	testutil.FailErr(t, "plan workers", err)
	if len(names) != 2 || !names["leg-1"] || !names["leg-2"] {
		t.Fatalf("plan workers = %v, want the two open legs", names)
	}
	other, err := env.svc.PlanWorkers(t.Context(), env.coord.SessionID, "feature.md")
	testutil.FailErr(t, "plan workers by another path", err)
	if len(other) != 0 {
		t.Fatalf("a different path matched this plan's workers: %v", other)
	}
}

func TestPlanningUnderAWorkflowRunBindsWorkersToIt(t *testing.T) {
	env := newPlanEnv(t)
	env.blueprintWith(featurePlan, featureTasks)
	_, err := env.plan(t, "before-run", featurePlan)
	testutil.FailErr(t, "plan without a run", err)

	env.workflowRun(t, "run-1", "implement")
	seeded, err := env.plan(t, "under-run", featurePlan)
	testutil.FailErr(t, "plan under a run", err)
	if seeded.WorkflowRunID != "run-1" || seeded.WorkflowPhase != "implement" {
		t.Fatalf("seeded = %+v", seeded)
	}
	planned, err := env.records.ByBlueprint(t.Context(), env.coord, featurePlan)
	testutil.FailErr(t, "by blueprint", err)
	for _, w := range planned {
		if w.WorkflowRunID != "run-1" || w.WorkflowPhase != "implement" {
			t.Fatalf("worker %s stayed outside the run: %+v", w.Name, w)
		}
	}
}

func TestMalformedBlueprintIsRefusedWithItsGraphReason(t *testing.T) {
	env := newPlanEnv(t)
	env.blueprintWith(featurePlan, `[{"id":"a","files":["a.go"],"depends_on":["missing"]}]`)
	_, err := env.plan(t, "op-1", featurePlan)
	data := wantReject(t, err, "dependency_unknown")
	if data["task"] != "a" || data["other_task"] != "missing" || data["blueprint_path"] != featurePlan {
		t.Fatalf("rejection data = %v", data)
	}
}

func TestOnlyTheCoordinatorPlansFromABlueprint(t *testing.T) {
	env := newPlanEnv(t)
	env.blueprintWith(featurePlan, featureTasks)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", workerplan.RegisterPlanFromBlueprintTool(reg, env.svc))
	args := map[string]any{"blueprint_path": featurePlan}

	_, err := reg.Run(t.Context(), "plan_from_blueprint", args, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child-1", ParentSessionID: env.coord.SessionID, Agent: "implementer"},
	})
	wantReject(t, err, "coordinator_only")

	raw, err := reg.Run(t.Context(), "plan_from_blueprint", args, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: env.coord.SessionID, ProjectID: testdbseed.DefaultProjectID, Agent: "coordinator", ToolCallID: "call-1"},
	})
	testutil.FailErr(t, "coordinator call", err)
	var seeded workerplan.SeedResult
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(raw), &seeded))
	if len(seeded.Legs) != 3 {
		t.Fatalf("seeded = %+v", seeded)
	}
}

func TestAPlanServiceNeedsEveryCollaborator(t *testing.T) {
	env := newPlanEnv(t)
	for name, drop := range map[string]func(*workerplan.Deps){
		"Records":       func(d *workerplan.Deps) { d.Records = nil },
		"Plans":         func(d *workerplan.Deps) { d.Plans = nil },
		"Approvals":     func(d *workerplan.Deps) { d.Approvals = nil },
		"WorkflowReady": func(d *workerplan.Deps) { d.WorkflowReady = nil },
		"Runs":          func(d *workerplan.Deps) { d.Runs = nil },
		"ActiveRuns":    func(d *workerplan.Deps) { d.ActiveRuns = nil },
		"HeadSHA":       func(d *workerplan.Deps) { d.HeadSHA = nil },
		"GitDiff":       func(d *workerplan.Deps) { d.GitDiff = nil },
		"SourceRuns":    func(d *workerplan.Deps) { d.SourceRuns = nil },
		"Jobs":          func(d *workerplan.Deps) { d.Jobs = nil },
		"Security":      func(d *workerplan.Deps) { d.Security = nil },
		"HostDataDir":   func(d *workerplan.Deps) { d.HostDataDir = nil },
	} {
		deps := env.deps()
		drop(&deps)
		if svc, err := workerplan.New(deps); svc != nil || !errors.Is(err, workerplan.ErrIncomplete) {
			t.Fatalf("New without %s = (%v, %v), want ErrIncomplete", name, svc, err)
		}
	}
}
