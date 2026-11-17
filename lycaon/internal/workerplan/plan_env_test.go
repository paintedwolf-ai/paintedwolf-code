package workerplan_test

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/workerplan"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

type fakePlans struct {
	docs map[string]*api.Blueprint
}

func (p *fakePlans) Get(_ context.Context, _, path string) (*api.Blueprint, error) {
	doc, ok := p.docs[path]
	if !ok {
		return nil, blueprint.ErrNotFound
	}
	return doc, nil
}

type fakeApprovals struct{ approved bool }

func (a *fakeApprovals) IsApproved(context.Context, string, string) (bool, error) {
	return a.approved, nil
}

type fakeReady struct{ ready bool }

func (r *fakeReady) ImplementWorkflowReady(context.Context, string, string) (bool, error) {
	return r.ready, nil
}

type fakeRuns struct {
	active      *api.WorkflowRun
	notRunnable error
}

func (r *fakeRuns) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return r.active, nil
}

func (r *fakeRuns) AssertRunnable(context.Context, string) error { return r.notRunnable }

type fakeHead struct{ sha string }

func (h *fakeHead) HeadSHA(context.Context, string) (string, error) { return h.sha, nil }

type fakeDiff struct {
	uncommitted map[string]bool
	sinceBase   map[string]bool
}

func (d *fakeDiff) HasUncommittedDiff(_ context.Context, _, path string) (bool, error) {
	return d.uncommitted[path], nil
}

func (d *fakeDiff) DiffFromRef(_ context.Context, _, _, path string) (bool, error) {
	return d.sinceBase[path], nil
}

type fakeSourceRuns struct {
	runs map[string][]workercompletion.WorkerInvocationReceipt
	err  error
}

func (s *fakeSourceRuns) WorkerSourceRuns(_ context.Context, child string) ([]workercompletion.WorkerInvocationReceipt, error) {
	return s.runs[child], s.err
}

type fakeJobs map[string]*api.WorkerTask

func (j fakeJobs) Get(id string) (*api.WorkerTask, bool) {
	task, ok := j[id]
	return task, ok
}

type fakeSecurity struct{ err error }

func (s *fakeSecurity) CheckSession(context.Context, string, string) error { return s.err }

// planEnv is a plan service over a real store with every collaborator faked:
// an approved, ready session with no workflow run, no diffs, no receipts and
// a clear scan gate.
type planEnv struct {
	db        *db.Store
	records   *workerrecords.Store
	coord     workerrecords.Actor
	plans     *fakePlans
	approvals *fakeApprovals
	ready     *fakeReady
	runs      *fakeRuns
	head      *fakeHead
	diff      *fakeDiff
	source    *fakeSourceRuns
	jobs      fakeJobs
	security  *fakeSecurity
	svc       *workerplan.Service
}

func (e *planEnv) deps() workerplan.Deps {
	return workerplan.Deps{
		Records: e.records, Plans: e.plans, Approvals: e.approvals, WorkflowReady: e.ready,
		Runs: e.runs, ActiveRuns: e.runs, HeadSHA: e.head, GitDiff: e.diff, SourceRuns: e.source,
		Jobs: e.jobs, Security: e.security,
		HostDataDir: func(context.Context, string) (string, error) { return "/host", nil },
	}
}

func newPlanEnv(t *testing.T) *planEnv {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "workerplan.db")
	const sessionID = "coord-plan"
	testdbseed.InsertSession(t, sqlDB, sessionID, testdbseed.DefaultProjectID)
	e := &planEnv{
		db:        sqlDB,
		records:   workerrecords.New(sqlDB),
		coord:     workerrecords.Actor{SessionID: sessionID, Role: workerrecords.RoleCoordinator, PrincipalID: sessionID},
		plans:     &fakePlans{docs: map[string]*api.Blueprint{}},
		approvals: &fakeApprovals{approved: true},
		ready:     &fakeReady{ready: true},
		runs:      &fakeRuns{},
		head:      &fakeHead{sha: "abc1234"},
		diff:      &fakeDiff{},
		source:    &fakeSourceRuns{},
		jobs:      fakeJobs{},
		security:  &fakeSecurity{},
	}
	svc, err := workerplan.New(e.deps())
	testutil.FailErr(t, "new plan service", err)
	e.svc = svc
	return e
}

// blueprintWith stores a Blueprint whose task block is tasksJSON.
func (e *planEnv) blueprintWith(path, tasksJSON string) {
	e.plans.docs[path] = &api.Blueprint{Path: path, Content: "<!-- lycaon:tasks " + tasksJSON + " -->"}
}

func (e *planEnv) plan(t *testing.T, op, path string) (workerplan.SeedResult, error) {
	t.Helper()
	return e.svc.PlanFromBlueprint(t.Context(), workerplan.SeedRequest{
		Actor: e.coord, OperationID: op, ProjectID: testdbseed.DefaultProjectID, WorkspacePath: "/repo", BlueprintPath: path,
	})
}

func (e *planEnv) worker(t *testing.T, spec workerrecords.WorkerSpec) workerrecords.Worker {
	t.Helper()
	if spec.AgentType == "" {
		spec.AgentType = "implementer"
	}
	if spec.ScopeMode == "" {
		spec.ScopeMode = workerrecords.ScopeWrite
	}
	created, err := e.records.CreateWorker(t.Context(), e.coord, workerrecords.Op{ID: "create:" + spec.Name}, spec)
	testutil.FailErr(t, "create worker "+spec.Name, err)
	w, err := e.records.Worker(t.Context(), e.coord, created.WorkerID)
	testutil.FailErr(t, "load worker "+spec.Name, err)
	return w
}

// workflowRun makes runID the session's active run in phase.
func (e *planEnv) workflowRun(t *testing.T, runID, phase string) {
	t.Helper()
	now := timelayout.Format(time.Now().UTC())
	_, err := e.db.ExecContext(t.Context(), `INSERT INTO workflow_runs (
			id, project_id, session_id, workflow_id, workflow_version, status, current_phase,
			revision, created_at, updated_at
		) VALUES (?, ?, ?, 'implement', '1.0.0', 'running', ?, 1, ?, ?)`,
		runID, testdbseed.DefaultProjectID, e.coord.SessionID, phase, now, now)
	testutil.FailErr(t, "insert workflow run "+runID, err)
	e.runs.active = &api.WorkflowRun{ID: runID, CurrentPhase: phase}
}

// startRound admits a round of workerID on a new job.
func (e *planEnv) startRound(t *testing.T, workerID, jobID string) {
	t.Helper()
	now := timelayout.Format(time.Now().UTC())
	_, err := e.db.ExecContext(t.Context(), `INSERT INTO worker_jobs (
			id, project_id, parent_session_id, agent_type, status, prompt, brief, created_at
		) VALUES (?, ?, ?, 'implementer', 'running', 'prompt', 'brief', ?)`,
		jobID, testdbseed.DefaultProjectID, e.coord.SessionID, now)
	testutil.FailErr(t, "insert job "+jobID, err)
	_, err = e.records.AdmitRound(t.Context(), e.coord, workerrecords.Op{ID: "round:" + jobID}, workerID, workerrecords.RoundSpec{
		JobID: jobID, ExecutionTarget: "local",
	})
	testutil.FailErr(t, "admit round "+jobID, err)
}

// settleRound delivers jobID's result and returns the result id.
func (e *planEnv) settleRound(t *testing.T, jobID string) string {
	t.Helper()
	now := timelayout.Format(time.Now().UTC())
	resultID := "res-" + jobID
	_, err := e.db.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES (?, ?, 'complete', ?)`,
		resultID, jobID, now)
	testutil.FailErr(t, "insert result "+jobID, err)
	_, err = db.New(e.db).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{SettledAt: db.NullString(now), JobID: jobID})
	testutil.FailErr(t, "settle round "+jobID, err)
	return resultID
}

func (e *planEnv) recordOutcomes(t *testing.T, resultID string, outcomes ...workerrecords.CriterionOutcome) {
	t.Helper()
	_, err := db.New(e.db).RecordWorkerResultCriteriaOutcomes(t.Context(), db.RecordWorkerResultCriteriaOutcomesParams{
		CriteriaOutcomesJson: workerrecords.EncodeCriteriaOutcomes(outcomes), ResultID: resultID,
	})
	testutil.FailErr(t, "record outcomes "+resultID, err)
}

// wantReject fails unless err is a rejection carrying reason; it returns the
// rejection's data.
func wantReject(t *testing.T, err error, reason string) map[string]any {
	t.Helper()
	rej := toolrejection.AsToolReject(err)
	if rej == nil || rej.Data["reason"] != reason {
		t.Fatalf("error = %v, want a rejection with reason %q", err, reason)
	}
	return rej.Data
}
