package workerplan_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type stubPlans struct {
	docs map[string]*api.Blueprint
}

func (p *stubPlans) Get(_ context.Context, _, path string) (*api.Blueprint, error) {
	doc, ok := p.docs[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return doc, nil
}

type stubApprovals struct {
	approved bool
	err      error
}

func (a *stubApprovals) IsApproved(_ context.Context, _, _ string) (bool, error) {
	return a.approved, a.err
}

type stubWorkflowReady struct {
	ready bool
	err   error
}

func (r *stubWorkflowReady) ImplementWorkflowReady(_ context.Context, _, _ string) (bool, error) {
	return r.ready, r.err
}

type stubRuns struct {
	active *api.WorkflowRun
	err    error
}

func (r *stubRuns) ActiveBySession(_ context.Context, _ string) (*api.WorkflowRun, error) {
	return r.active, nil
}

func (r *stubRuns) AssertRunnable(_ context.Context, _ string) error {
	return r.err
}

type stubHeadSHA struct {
	sha string
}

func (h stubHeadSHA) HeadSHA(_ context.Context, _ string) (string, error) {
	return h.sha, nil
}

type planTestEnv struct {
	sqlDB     *db.Store
	records   *workerrecords.Store
	coord     workerrecords.Actor
	plans     *stubPlans
	approvals *stubApprovals
	ready     *stubWorkflowReady
	runs      *stubRuns
	svc       *workerplan.Service
}

func newPlanTestEnv(t *testing.T) *planTestEnv {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "workerplan.db")
	const sessionID = "coord-plan"
	testdbseed.InsertSession(t, sqlDB, sessionID, testdbseed.DefaultProjectID)
	records := workerrecords.New(sqlDB)
	plans := &stubPlans{docs: map[string]*api.Blueprint{}}
	approvals := &stubApprovals{approved: true}
	ready := &stubWorkflowReady{ready: true}
	runs := &stubRuns{}
	return &planTestEnv{
		sqlDB:     sqlDB,
		records:   records,
		coord:     workerrecords.Actor{SessionID: sessionID, Role: workerrecords.RoleCoordinator, PrincipalID: sessionID},
		plans:     plans,
		approvals: approvals,
		ready:     ready,
		runs:      runs,
		svc: &workerplan.Service{
			Records:       records,
			Plans:         plans,
			Approvals:     approvals,
			WorkflowReady: ready,
			Runs:          runs,
			ActiveRuns:    runs,
			HeadSHA:       stubHeadSHA{sha: "abc1234"},
		},
	}
}

func (e *planTestEnv) insertJobAndRound(t *testing.T, workerID, jobID string, settle bool) {
	t.Helper()
	now := db.FormatTime(time.Now().UTC())
	_, err := e.sqlDB.ExecContext(t.Context(), `INSERT INTO worker_jobs (
			id, project_id, parent_session_id, agent_type, status, prompt, brief, created_at
		) VALUES (?, ?, ?, 'implementer', 'running', 'prompt', 'brief', ?)`,
		jobID, testdbseed.DefaultProjectID, e.coord.SessionID, now)
	testutil.FailErr(t, "insert job "+jobID, err)
	_, err = e.records.AdmitRound(t.Context(), e.coord, workerrecords.Op{ID: "round:" + jobID}, workerID, workerrecords.RoundSpec{
		JobID:           jobID,
		ExecutionTarget: "local",
	})
	testutil.FailErr(t, "admit round "+jobID, err)
	if settle {
		_, err = e.sqlDB.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES (?, ?, 'complete', ?)`,
			"res-"+jobID, jobID, now)
		testutil.FailErr(t, "insert result "+jobID, err)
		_, err = db.New(e.sqlDB).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{
			SettledAt: db.NullString(now),
			JobID:     jobID,
		})
		testutil.FailErr(t, "settle round "+jobID, err)
	}
}

func (e *planTestEnv) insertWorkflowRun(t *testing.T, runID, phase string) {
	t.Helper()
	now := db.FormatTime(time.Now().UTC())
	_, err := e.sqlDB.ExecContext(t.Context(), `INSERT INTO workflow_runs (
			id, project_id, session_id, workflow_id, workflow_version, status, current_phase,
			revision, created_at, updated_at
		) VALUES (?, ?, ?, 'implement', '1.0.0', 'running', ?, 1, ?, ?)`,
		runID, testdbseed.DefaultProjectID, e.coord.SessionID, phase, now, now)
	testutil.FailErr(t, "insert workflow_run "+runID, err)
}

func TestParseBlueprintLegsAndPathOverlap(t *testing.T) {
	legs, err := workerplan.ParseBlueprintLegs(&api.Blueprint{Title: "Add caching", Content: "# Plan\nNo task block."})
	testutil.FailErr(t, "default leg", err)
	if len(legs) != 1 || legs[0].WorkID != "plan-1" || legs[0].Files[0] != "**/*" {
		t.Fatalf("default legs = %+v", legs)
	}

	emptyMarker, err := workerplan.ParseBlueprintLegs(&api.Blueprint{Content: "<!-- lycaon:tasks   -->"})
	testutil.FailErr(t, "empty marker", err)
	if len(emptyMarker) != 1 {
		t.Fatalf("empty marker legs = %+v", emptyMarker)
	}

	errCases := []struct {
		name    string
		content string
	}{
		{name: "unclosed", content: "<!-- lycaon:tasks [{\"id\":\"a\"}]"},
		{name: "bad_json", content: "<!-- lycaon:tasks {bad -->"},
		{name: "bad_id", content: `<!-- lycaon:tasks [{"id":" bad ","title":"t","files":["a.go"]}] -->`},
		{name: "dup_id", content: `<!-- lycaon:tasks [{"id":"a","title":"1","files":["a.go"]},{"id":"a","title":"2","files":["b.go"]}] -->`},
		{name: "self_dep", content: `<!-- lycaon:tasks [{"id":"a","title":"1","files":["a.go"],"depends_on":["a"]}] -->`},
		{name: "unknown_dep", content: `<!-- lycaon:tasks [{"id":"a","title":"1","files":["a.go"],"depends_on":["missing"]}] -->`},
		{name: "cycle", content: `<!-- lycaon:tasks [{"id":"a","title":"1","files":["a.go"],"depends_on":["b"]},{"id":"b","title":"2","files":["b.go"],"depends_on":["a"]}] -->`},
		{name: "unordered_overlap", content: `<!-- lycaon:tasks [{"id":"a","title":"1","files":["pkg/x.go"]},{"id":"b","title":"2","files":["pkg/*"]}] -->`},
	}
	for _, tc := range errCases {
		if _, pErr := workerplan.ParseBlueprintLegs(&api.Blueprint{Content: tc.content}); pErr == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}

	ordered := `<!-- lycaon:tasks [
		{"id":"leg-a","title":"Write core","files":["internal/core/a.go"],"verify":["go test ./internal/core/..."]},
		{"id":"leg-b","title":"Extend core","files":["internal/core/*"],"depends_on":["leg-a"]}
	] -->`
	parsed, err := workerplan.ParseBlueprintLegs(&api.Blueprint{Content: ordered})
	testutil.FailErr(t, "ordered overlap", err)
	if len(parsed) != 2 || len(parsed[0].Criteria) != 4 {
		t.Fatalf("ordered parsed = %+v", parsed)
	}
	if !workerplan.PathsOverlap([]string{"internal/a/**"}, []string{"internal/a/b/c.go"}) {
		t.Fatal("expected directory prefix overlap")
	}
	if workerplan.PathsOverlap([]string{"internal/a/x.go"}, []string{"internal/b/y.go"}) {
		t.Fatal("unexpected disjoint overlap")
	}
}

func TestPlanFromBlueprintIdempotentAndRevisionReconcile(t *testing.T) {
	env := newPlanTestEnv(t)
	ctx := t.Context()
	const bpPath = ".lycaon/plans/feature.md"
	env.plans.docs[bpPath] = &api.Blueprint{
		Path:  bpPath,
		Title: "Feature",
		Content: `<!-- lycaon:tasks [
			{"id":"leg-1","title":"Schema","files":["internal/db/a.go"],"verify":["go test ./internal/db/..."]},
			{"id":"leg-2","title":"Service","files":["internal/svc/b.go"],"depends_on":["leg-1"]},
			{"id":"leg-3","title":"Extra","files":["internal/extra/c.go"]}
		] -->`,
	}

	env.approvals.approved = false
	_, err := env.svc.PlanFromBlueprint(ctx, workerplan.SeedRequest{
		Actor: env.coord, OperationID: "op-unapproved", ProjectID: testdbseed.DefaultProjectID, BlueprintPath: bpPath,
	})
	if rej := toolrejection.AsToolReject(err); rej == nil || rej.Data["reason"] != "plan_not_approved" {
		t.Fatalf("unapproved error = %v", err)
	}
	env.approvals.approved = true
	env.ready.ready = false
	_, err = env.svc.PlanFromBlueprint(ctx, workerplan.SeedRequest{
		Actor: env.coord, OperationID: "op-unready", ProjectID: testdbseed.DefaultProjectID, BlueprintPath: bpPath,
	})
	if rej := toolrejection.AsToolReject(err); rej == nil || rej.Data["reason"] != "implement_workflow_not_ready" {
		t.Fatalf("unready error = %v", err)
	}
	env.ready.ready = true

	first, err := env.svc.PlanFromBlueprint(ctx, workerplan.SeedRequest{
		Actor: env.coord, OperationID: "op-1", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: "/repo", BlueprintPath: bpPath,
	})
	testutil.FailErr(t, "first seed", err)
	if first.BaseSHA != "abc1234" || len(first.Legs) != 3 || len(first.Legs[1].AfterWorkers) != 1 || first.Legs[1].AfterWorkers[0] != "leg-1" {
		t.Fatalf("first seed = %+v", first)
	}

	replay, err := env.svc.PlanFromBlueprint(ctx, workerplan.SeedRequest{
		Actor: env.coord, OperationID: "op-2", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: "/repo", BlueprintPath: bpPath,
	})
	testutil.FailErr(t, "idempotent replay", err)
	if replay.BlueprintRevision != first.BlueprintRevision || len(replay.Legs) != 3 {
		t.Fatalf("replay = %+v", replay)
	}

	workers, err := env.records.ByBlueprint(ctx, env.coord, bpPath)
	testutil.FailErr(t, "by blueprint", err)
	var leg1ID string
	for _, w := range workers {
		if w.WorkflowWorkID == "leg-1" {
			leg1ID = w.ID
		}
	}
	env.insertJobAndRound(t, leg1ID, "job-leg-1", true)

	env.plans.docs[bpPath] = &api.Blueprint{
		Path:  bpPath,
		Title: "Feature v2",
		Content: `<!-- lycaon:tasks [
			{"id":"leg-1","title":"Schema edited after start","files":["internal/db/other.go"]},
			{"id":"leg-2","title":"Service updated","files":["internal/svc/b2.go"],"depends_on":["leg-1"]}
		] -->`,
	}
	reconciled, err := env.svc.PlanFromBlueprint(ctx, workerplan.SeedRequest{
		Actor: env.coord, OperationID: "op-3", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: "/repo", BlueprintPath: bpPath,
	})
	testutil.FailErr(t, "reconcile v2", err)
	if len(reconciled.Withdrawn) != 1 || reconciled.Withdrawn[0] != "leg-3" {
		t.Fatalf("withdrawn = %+v", reconciled.Withdrawn)
	}
	if reconciled.Legs[0].State != "completed" || reconciled.Legs[0].Files[0] != "internal/db/a.go" {
		t.Fatalf("completed leg-1 was disturbed: %+v", reconciled.Legs[0])
	}
	if reconciled.Legs[1].State != "planned" || reconciled.Legs[1].Files[0] != "internal/svc/b2.go" {
		t.Fatalf("unstarted leg-2 was not updated: %+v", reconciled.Legs[1])
	}

	planSet, err := env.svc.PlanWorkers(ctx, env.coord.SessionID, "feature.md")
	testutil.FailErr(t, "plan workers", err)
	if !planSet["leg-1"] || !planSet["leg-2"] || planSet["leg-3"] {
		t.Fatalf("plan workers = %+v", planSet)
	}
}

func TestPlanToolGuardAndRoundAdmissionGates(t *testing.T) {
	env := newPlanTestEnv(t)
	ctx := t.Context()
	const bpPath = ".lycaon/plans/gates.md"
	env.insertWorkflowRun(t, "run-1", "implement")
	env.runs.active = &api.WorkflowRun{ID: "run-1", CurrentPhase: "implement"}
	env.plans.docs[bpPath] = &api.Blueprint{
		Path: bpPath,
		Content: `<!-- lycaon:tasks [
			{"id":"w1","title":"First","files":["pkg/a.go"]},
			{"id":"w2","title":"Second","files":["pkg/b.go"],"depends_on":["w1"]}
		] -->`,
	}

	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register tool", workerplan.RegisterPlanFromBlueprintTool(reg, env.svc))

	workerCall := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child-1", ParentSessionID: env.coord.SessionID, Agent: "implementer"},
	}
	if _, err := reg.Run(ctx, "plan_from_blueprint", map[string]any{"blueprint_path": bpPath}, workerCall); toolrejection.AsToolReject(err) == nil {
		t.Fatalf("expected worker call rejection, got %v", err)
	}

	coordCall := tools.ToolContext{
		Identity: tools.InvocationIdentity{
			SessionID:  env.coord.SessionID,
			ProjectID:  testdbseed.DefaultProjectID,
			Agent:      "coordinator",
			ToolCallID: "call-1",
		},
	}
	raw, err := reg.Run(ctx, "plan_from_blueprint", map[string]any{"blueprint_path": bpPath}, coordCall)
	testutil.FailErr(t, "coord plan_from_blueprint", err)
	var seeded workerplan.SeedResult
	testutil.FailErr(t, "decode seeded", json.Unmarshal([]byte(raw), &seeded))
	if seeded.WorkflowRunID != "run-1" || len(seeded.Legs) != 2 {
		t.Fatalf("seeded = %+v", seeded)
	}

	has, err := env.svc.HasPlannedWorker(ctx, env.coord.SessionID, "w2")
	testutil.FailErr(t, "has planned worker", err)
	if !has {
		t.Fatal("expected planned worker w2")
	}
	work, ok, err := env.svc.ResolvePlannedWorkflowWork(ctx, env.coord.SessionID, "w1")
	testutil.FailErr(t, "resolve planned work", err)
	if !ok || work.AgentType != "implementer" || work.RunID != "run-1" {
		t.Fatalf("resolved work = %+v, ok=%v", work, ok)
	}

	_, _, _, err = env.svc.PreparePlannedTask(ctx, env.coord, testdbseed.DefaultProjectID, "w2", workerrecords.ScopeWrite, nil, nil)
	if rej := toolrejection.AsToolReject(err); rej == nil || rej.Data["reason"] != "prerequisite_worker_not_started" {
		t.Fatalf("expected prerequisite_worker_not_started, got %v", err)
	}

	w1, afterW1, found, err := env.svc.PreparePlannedTask(ctx, env.coord, testdbseed.DefaultProjectID, "w1", workerrecords.ScopeWrite, nil, nil)
	testutil.FailErr(t, "prepare w1", err)
	if !found || len(afterW1) != 0 {
		t.Fatalf("prepare w1 = %+v, after=%v", w1, afterW1)
	}
	env.insertJobAndRound(t, w1.ID, "job-w1", false)

	_, _, _, err = env.svc.PreparePlannedTask(ctx, env.coord, testdbseed.DefaultProjectID, "w1", workerrecords.ScopeWrite, nil, nil)
	if rej := toolrejection.AsToolReject(err); rej == nil || rej.Data["reason"] != "planned_worker_already_started" {
		t.Fatalf("expected planned_worker_already_started, got %v", err)
	}

	_, afterW2, found, err := env.svc.PreparePlannedTask(ctx, env.coord, testdbseed.DefaultProjectID, "w2", workerrecords.ScopeWrite, []string{"pkg/a.go"}, nil)
	testutil.FailErr(t, "prepare w2 ordered overlap", err)
	if !found || len(afterW2) != 1 || afterW2[0] != "w1" {
		t.Fatalf("prepare w2 after = %v", afterW2)
	}

	extra, err := env.records.CreateWorker(ctx, env.coord, workerrecords.Op{ID: "extra"}, workerrecords.WorkerSpec{
		Name: "rogue", AgentType: "implementer", ScopeMode: workerrecords.ScopeWrite, BlueprintPath: bpPath, Files: []string{"pkg/a.go"},
	})
	testutil.FailErr(t, "create extra", err)
	extraW, err := env.records.Worker(ctx, env.coord, extra.WorkerID)
	testutil.FailErr(t, "load extra", err)
	err = env.svc.CheckRoundAdmission(ctx, workerplan.RoundAdmissionRequest{
		Actor: env.coord, Tool: "task", ProjectID: testdbseed.DefaultProjectID, Worker: extraW, ScopeMode: workerrecords.ScopeWrite,
	})
	if rej := toolrejection.AsToolReject(err); rej == nil || rej.Data["reason"] != "blueprint_write_path_overlap" {
		t.Fatalf("expected blueprint_write_path_overlap, got %v", err)
	}

	env.runs.err = errors.New("workflow paused")
	err = env.svc.CheckRoundAdmission(ctx, workerplan.RoundAdmissionRequest{
		Actor: env.coord, Tool: "instruct_worker", ProjectID: testdbseed.DefaultProjectID, Worker: w1, ScopeMode: workerrecords.ScopeWrite,
	})
	if rej := toolrejection.AsToolReject(err); rej == nil || !strings.Contains(rej.Data["reason"].(string), "workflow paused") {
		t.Fatalf("expected workflow hold rejection, got %v", err)
	}
	env.runs.err = nil

	now := db.FormatTime(time.Now().UTC())
	_, err = db.New(env.sqlDB).SettleWorkerRoundByJob(ctx, db.SettleWorkerRoundByJobParams{SettledAt: db.NullString(now), JobID: "job-w1"})
	testutil.FailErr(t, "settle w1", err)
	testutil.FailErr(t, "supersede settled w1", env.svc.SupersedeSettledWorkflowWorker(ctx, env.coord, workerrecords.Op{ID: "sup-w1"}, "run-1", "implement", "w1"))
	reloadedW1, err := env.records.Worker(ctx, env.coord, w1.ID)
	testutil.FailErr(t, "reload w1", err)
	if reloadedW1.ClosedReason != "superseded" {
		t.Fatalf("closed reason = %q, want superseded", reloadedW1.ClosedReason)
	}
}
