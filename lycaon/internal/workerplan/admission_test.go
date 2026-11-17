package workerplan_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

const gatesPlan = ".lycaon/plans/gates.md"

func (e *planEnv) prepare(t *testing.T, workID string) (workerrecords.Worker, []string, bool, error) {
	t.Helper()
	return e.svc.PreparePlannedTask(t.Context(), e.coord, testdbseed.DefaultProjectID, workID, workerrecords.ScopeWrite, nil, nil)
}

func TestPlannedTaskRunsAfterItsPrerequisites(t *testing.T) {
	env := newPlanEnv(t)
	env.blueprintWith(gatesPlan, `[
		{"id":"w1","title":"First","files":["pkg/a.go"]},
		{"id":"w2","title":"Second","files":["pkg/a.go"],"depends_on":["w1"]}
	]`)
	_, err := env.plan(t, "op-1", gatesPlan)
	testutil.FailErr(t, "plan", err)

	_, _, _, err = env.prepare(t, "w2")
	data := wantReject(t, err, "prerequisite_worker_not_started")
	if data["prerequisite_worker"] != "w1" {
		t.Fatalf("rejection data = %v", data)
	}

	w1, after, found, err := env.prepare(t, "w1")
	testutil.FailErr(t, "prepare w1", err)
	if !found || len(after) != 0 {
		t.Fatalf("w1 = %+v after %v, found %v", w1, after, found)
	}
	env.startRound(t, w1.ID, "job-w1")

	// w2 writes the path w1 is still writing; the plan orders them.
	_, after, found, err = env.prepare(t, "w2")
	testutil.FailErr(t, "prepare w2", err)
	if !found || !slices.Equal(after, []string{"w1"}) {
		t.Fatalf("w2 runs after %v, found %v; want after w1", after, found)
	}

	_, _, _, err = env.prepare(t, "w1")
	data = wantReject(t, err, "planned_worker_already_started")
	if data["worker"] != "w1" {
		t.Fatalf("rejection data = %v", data)
	}
}

func TestAWorkIDWithNoPlannedWorkerIsOrdinaryWork(t *testing.T) {
	env := newPlanEnv(t)
	env.worker(t, workerrecords.WorkerSpec{Name: "ad-hoc"})
	for _, workID := range []string{"", "unplanned"} {
		_, _, found, err := env.prepare(t, workID)
		testutil.FailErr(t, "prepare "+workID, err)
		if found {
			t.Fatalf("work id %q resolved to a planned worker", workID)
		}
		has, err := env.svc.HasPlannedWorker(t.Context(), env.coord.SessionID, workID)
		testutil.FailErr(t, "has planned "+workID, err)
		if has {
			t.Fatalf("work id %q reported a planned worker", workID)
		}
	}
}

func TestPlannedWorkerResolvesWithinTheActiveRun(t *testing.T) {
	env := newPlanEnv(t)
	env.blueprintWith(gatesPlan, `[{"id":"w1","title":"First","files":["pkg/a.go"]}]`)
	_, err := env.plan(t, "before-run", gatesPlan)
	testutil.FailErr(t, "plan without a run", err)

	work, found, err := env.svc.ResolvePlannedWorkflowWork(t.Context(), env.coord.SessionID, "w1")
	testutil.FailErr(t, "resolve without a run", err)
	if !found || work.RunID != "" || work.AgentType != "implementer" || !slices.Equal(work.Scope.Paths, []string{"pkg/a.go"}) {
		t.Fatalf("work = %+v, found %v", work, found)
	}

	// A run starts: the worker planned before it belongs to no run, and is
	// refused until the plan is staged again under the run.
	env.workflowRun(t, "run-1", "implement")
	_, _, err = env.svc.ResolvePlannedWorkflowWork(t.Context(), env.coord.SessionID, "w1")
	data := wantReject(t, err, "planned_worker_other_run")
	if data["worker"] != "w1" {
		t.Fatalf("rejection data = %v", data)
	}

	_, err = env.plan(t, "under-run", gatesPlan)
	testutil.FailErr(t, "plan under the run", err)
	work, found, err = env.svc.ResolvePlannedWorkflowWork(t.Context(), env.coord.SessionID, "w1")
	testutil.FailErr(t, "resolve under the run", err)
	if !found || work.RunID != "run-1" || work.Phase != "implement" {
		t.Fatalf("work = %+v, found %v", work, found)
	}
}

func TestTheActivePhaseDecidesBetweenWorkersOfOneRun(t *testing.T) {
	env := newPlanEnv(t)
	env.workflowRun(t, "run-1", "review")
	env.worker(t, workerrecords.WorkerSpec{Name: "critic-draft", WorkflowRunID: "run-1", WorkflowPhase: "draft", WorkflowWorkID: "critic"})
	env.worker(t, workerrecords.WorkerSpec{Name: "critic-review", WorkflowRunID: "run-1", WorkflowPhase: "review", WorkflowWorkID: "critic"})

	w, _, found, err := env.prepare(t, "critic")
	testutil.FailErr(t, "prepare in review", err)
	if !found || w.Name != "critic-review" {
		t.Fatalf("worker = %+v, found %v; want the review phase's", w, found)
	}

	env.runs.active.CurrentPhase = "ship"
	_, _, _, err = env.prepare(t, "critic")
	data := wantReject(t, err, "planned_worker_ambiguous")
	names, _ := data["workers"].([]string)
	slices.Sort(names)
	if !slices.Equal(names, []string{"critic-draft", "critic-review"}) {
		t.Fatalf("rejection data = %v", data)
	}
}

func TestConcurrentBlueprintWorkersMayNotWriteTheSamePaths(t *testing.T) {
	env := newPlanEnv(t)
	running := env.worker(t, workerrecords.WorkerSpec{Name: "running", BlueprintPath: gatesPlan, WorkflowWorkID: "a", Files: []string{"pkg/a.go"}})
	env.startRound(t, running.ID, "job-running")
	next := env.worker(t, workerrecords.WorkerSpec{Name: "next", BlueprintPath: gatesPlan, WorkflowWorkID: "b", Files: []string{"pkg/*"}})
	admit := func(mode string, after ...string) error {
		return env.svc.CheckRoundAdmission(t.Context(), workerplan.RoundAdmissionRequest{
			Actor: env.coord, Tool: "task", ProjectID: testdbseed.DefaultProjectID, Worker: next, ScopeMode: mode, AfterNames: after,
		})
	}

	data := wantReject(t, admit(workerrecords.ScopeWrite), "blueprint_write_path_overlap")
	if data["worker"] != "next" || data["overlap_worker"] != "running" {
		t.Fatalf("rejection data = %v", data)
	}
	testutil.FailErr(t, "ordered after the running worker", admit(workerrecords.ScopeWrite, "running"))
	testutil.FailErr(t, "read round", admit(workerrecords.ScopeRead))

	env.settleRound(t, "job-running")
	testutil.FailErr(t, "after the other worker settled", admit(workerrecords.ScopeWrite))
}

func TestRoundsAreRefusedOnARunThatCannotTakeWork(t *testing.T) {
	env := newPlanEnv(t)
	env.workflowRun(t, "run-1", "implement")
	w := env.worker(t, workerrecords.WorkerSpec{Name: "bound", WorkflowRunID: "run-1", WorkflowPhase: "implement", WorkflowWorkID: "w1"})
	env.runs.notRunnable = &runstate.NotRunnableError{RunID: "run-1", Status: api.WorkflowRunStatusPaused, Reason: "paused"}

	err := env.svc.CheckRoundAdmission(t.Context(), workerplan.RoundAdmissionRequest{
		Actor: env.coord, Tool: "instruct_worker", ProjectID: testdbseed.DefaultProjectID, Worker: w,
	})
	data := wantReject(t, err, "workflow_run_not_runnable")
	if data["tool"] != "instruct_worker" || data["workflow_run_id"] != "run-1" || data["workflow_run_status"] != string(api.WorkflowRunStatusPaused) {
		t.Fatalf("rejection data = %v", data)
	}
}

func TestSupersedingClosesOnlyTheSettledWorkerOfThatWork(t *testing.T) {
	env := newPlanEnv(t)
	env.workflowRun(t, "run-1", "implement")
	done := env.worker(t, workerrecords.WorkerSpec{Name: "done", WorkflowRunID: "run-1", WorkflowPhase: "implement", WorkflowWorkID: "w1"})
	env.startRound(t, done.ID, "job-done")
	env.settleRound(t, "job-done")
	busy := env.worker(t, workerrecords.WorkerSpec{Name: "busy", WorkflowRunID: "run-1", WorkflowPhase: "implement", WorkflowWorkID: "w2"})
	env.startRound(t, busy.ID, "job-busy")

	for _, workID := range []string{"w1", "w2"} {
		testutil.FailErr(t, "supersede "+workID, env.svc.SupersedeSettledWorkflowWorker(t.Context(), env.coord, workerrecords.Op{ID: "sup-" + workID}, "run-1", "implement", workID))
	}
	for id, wantReason := range map[string]string{done.ID: "superseded", busy.ID: ""} {
		w, err := env.records.Worker(t.Context(), env.coord, id)
		testutil.FailErr(t, "reload", err)
		if w.ClosedReason != wantReason {
			t.Fatalf("worker %s closed as %q, want %q", w.Name, w.ClosedReason, wantReason)
		}
	}
}
