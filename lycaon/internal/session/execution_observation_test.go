package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

type executionCheckpoints struct{ pending []api.CheckpointEvent }

func (c *executionCheckpoints) ListPending(_ context.Context, id string, _ *api.CheckpointKind) ([]api.CheckpointEvent, error) {
	var result []api.CheckpointEvent
	for _, p := range c.pending {
		if p.SessionID == id {
			result = append(result, p)
		}
	}
	return result, nil
}

func TestExecutionObservationUsesApplicationOwners(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		t.Run(backend, func(t *testing.T) {
			var st session.Store = store.NewMemory()
			if backend == "sql" {
				database := testdbfixture.Open(t, "execution.db")
				testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
				st = store.NewSQL(database)
			}
			parent, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create root", err)
			child, err := st.CreateChild(t.Context(), parent, api.SpawnChildRequest{AgentType: "implementer"})
			testutil.FailErr(t, "create child", err)
			_, _, err = st.PutPromptSubmission(t.Context(), store.PromptSubmission{ID: "admission", SessionID: parent.ID, ProjectID: parent.ProjectID, Origin: store.PromptSubmissionOriginUser, SubmittedBy: parent.OwnerPersonID, InputDigest: "task", InputJSON: "{}"})
			testutil.FailErr(t, "admit task", err)
			manager := session.NewHost(st, session.Models{Client: nil, Provider: nil, Limits: settings.SessionLimits{}, Cost: nil}, nil)
			checkpoints := &executionCheckpoints{}
			manager.Observations.SetExecutionCheckpoints(checkpoints)
			check := func(want bool) {
				t.Helper()
				observation, err := manager.Observations.Observe(t.Context(), parent.ID, "admission")
				testutil.FailErr(t, "observe execution", err)
				if observation.Settled != want {
					t.Fatalf("settlement=%+v, want %v", observation, want)
				}
			}
			check(false)
			admission, _, err := st.ClaimPromptSubmission(t.Context(), "admission")
			testutil.FailErr(t, "claim prompt", err)
			testutil.FailErr(t, "complete prompt", st.FinishPromptSubmission(t.Context(), admission.ID, admission.ClaimToken, store.PromptSubmissionComplete, "{}", store.PromptSubmissionFailure{}))
			check(true)
			foreign, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create unrelated session", err)
			testutil.FailErr(t, "mark unrelated session busy", st.SetSessionStatus(t.Context(), foreign.ID, api.SessionStatusBusy))
			check(true)
			testutil.FailErr(t, "mark child busy", st.SetSessionStatus(t.Context(), child.ID, api.SessionStatusBusy))
			check(false)
			testutil.FailErr(t, "mark child idle", st.SetSessionStatus(t.Context(), child.ID, api.SessionStatusIdle))
			check(true)
			checkpoints.pending = []api.CheckpointEvent{{SessionID: child.ID, ID: "approval"}}
			check(false)
			checkpoints.pending = nil
			check(true)
			release := manager.Runner.Coordinator.CoordinatorLoop().Admission.BeginPromptExecution(t.Context(), child.ID)
			check(false)
			release()
			check(true)
			for _, id := range []string{"child-failed", "child-recovered"} {
				_, _, err := st.PutPromptSubmission(t.Context(), store.PromptSubmission{ID: id, SessionID: child.ID, ProjectID: child.ProjectID, Origin: store.PromptSubmissionOriginUser, SubmittedBy: child.OwnerPersonID, InputDigest: id, InputJSON: "{}"})
				testutil.FailErr(t, "admit child execution", err)
				check(false)
				receipt, _, err := st.ClaimPromptSubmission(t.Context(), id)
				testutil.FailErr(t, "claim child execution", err)
				status := store.PromptSubmissionComplete
				failure := store.PromptSubmissionFailure{}
				if id == "child-failed" {
					status = store.PromptSubmissionFailed
					failure.Code = "provider_overloaded"
				}
				testutil.FailErr(t, "settle child execution", st.FinishPromptSubmission(t.Context(), id, receipt.ClaimToken, status, "{}", failure))
				check(true)
				observation, err := manager.Observations.Observe(t.Context(), parent.ID, "admission")
				testutil.FailErr(t, "observe latest child receipt", err)
				if (len(observation.Failures) == 1) != (id == "child-failed") {
					t.Fatalf("stale failure projection: %+v", observation.Failures)
				}
			}
			q := worker.NewInMemoryQueue(1)
			manager.SetWorkerQueue(q)
			jobID, err := q.Enqueue(t.Context(), api.WorkerTask{ParentSessionID: parent.ID, ProjectID: parent.ProjectID, Prompt: "work", Brief: "work", ExecutionTarget: api.ExecutionTargetLocal})
			testutil.FailErr(t, "enqueue worker", err)
			check(false)
			job, err := q.ClaimNext(t.Context(), worker.ClaimRequest{ExecutionTarget: api.ExecutionTargetLocal})
			testutil.FailErr(t, "claim worker", err)
			_, err = q.Complete(t.Context(), job, api.WorkerResult{Status: "needs_decision"})
			testutil.FailErr(t, "return decision", err)
			check(false)
			testutil.FailErr(t, "deliver decision", q.MarkOutcomeDelivered(t.Context(), jobID))
			check(true)
			if _, err := manager.Observations.Observe(t.Context(), child.ID, "admission"); err == nil {
				t.Fatal("foreign admission accepted")
			}
		})
	}
}

func TestExecutionObservationRejectsMissingSession(t *testing.T) {
	st := store.NewMemory()
	_, _, err := st.PutPromptSubmission(t.Context(), store.PromptSubmission{ID: "orphan", SessionID: "missing", Origin: store.PromptSubmissionOriginUser, SubmittedBy: testutil.HostOwner().ID})
	testutil.FailErr(t, "retain orphan admission", err)
	manager := session.NewHost(st, session.Models{Client: nil, Provider: nil, Limits: settings.SessionLimits{}, Cost: nil}, nil)
	if _, err := manager.Observations.Observe(t.Context(), "missing", "orphan"); err == nil {
		t.Fatal("missing session became a wait state")
	}
}

func TestExecutionTreeSupportsWorkflowStartsWithoutPromptAdmission(t *testing.T) {
	st := store.NewMemory()
	root, err := st.Create(t.Context(), api.CreateSessionRequest{}, "project")
	testutil.FailErr(t, "create workflow session", err)
	manager := session.NewHost(st, session.Models{Client: nil, Provider: nil, Limits: settings.SessionLimits{}, Cost: nil}, nil)
	observation, err := manager.Observations.Tree(t.Context(), root.ID)
	testutil.FailErr(t, "observe idle tree", err)
	if !observation.Settled || observation.SubmissionID != "" {
		t.Fatalf("workflow tree observation: %+v", observation)
	}
	release := manager.Runner.Coordinator.CoordinatorLoop().Admission.BeginPromptExecution(t.Context(), root.ID)
	defer release()
	observation, err = manager.Observations.Tree(t.Context(), root.ID)
	testutil.FailErr(t, "observe active workflow", err)
	if observation.Settled {
		t.Fatal("active workflow prompt was considered settled")
	}
	if _, err := manager.Observations.Tree(t.Context(), "missing"); err == nil {
		t.Fatal("missing workflow session accepted")
	}
}
