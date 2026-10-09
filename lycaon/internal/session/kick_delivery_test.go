package session_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/oartest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// budgetJobs serves fixed worker jobs by id.
type budgetJobs map[string]*api.WorkerTask

func (budgetJobs) ListBySession(context.Context, string, string, ...api.WorkerStatus) ([]api.WorkerTask, error) {
	return nil, nil
}

func (budgetJobs) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) { return nil, nil }

func (j budgetJobs) Get(jobID string) (*api.WorkerTask, bool) {
	task, ok := j[jobID]
	return task, ok
}

func (budgetJobs) ClaimWorkerBranch(context.Context, string) (*api.WorkerTask, error) {
	return nil, nil
}

func TestTurnDeliversEveryQueuedKickAndDropsAnsweredBudgetRequests(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	st := store.NewMemory()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewManager(st, rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	testutil.FailErr(t, "install anchor registry", mgr.Guidance.InstallAnchorRegistry())
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	request := &api.WorkerBudgetRequest{Rounds: 4, RequestedMax: 24, RemainingWork: []string{"trace the alternate callers"}, ToolLoopsUsed: 15, RequestedAt: time.Now().UTC()}
	mgr.SetWorkerQueue(budgetJobs{
		"job-open":     {ID: "job-open", Status: api.WorkerStatusRunning, MaxToolLoops: 20, BudgetRequest: request},
		"job-answered": {ID: "job-answered", Status: api.WorkerStatusRunning, MaxToolLoops: 24},
	})

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.Guidance.Emit(ctx, sess.ID, anchor.ComposeDone, anchor.Envelope{})
	for _, job := range []string{"job-answered", "job-open"} {
		facts := kick.WorkerBudgetFacts{JobID: job, Used: 15, Max: 20, HostMax: 120, Request: request}
		mgr.Guidance.Emit(ctx, sess.ID, anchor.WorkerBudgetRequested, anchor.Envelope{Subject: job, WorkerBudget: &facts})
	}
	if _, err := mgr.Submissions.Prompt(ctx, sess.ID, "continue"); err != nil {
		testutil.FailErr(t, "prompt", err)
	}
	var delivered []string
	for _, msg := range rec.LastRequest().Messages {
		switch {
		case strings.Contains(msg.Content, "Compose succeeded"):
			delivered = append(delivered, "compose")
		case strings.Contains(msg.Content, "Worker `job-open` asked"):
			delivered = append(delivered, "job-open")
		case strings.Contains(msg.Content, "Worker `job-answered` asked"):
			delivered = append(delivered, "job-answered")
		}
	}
	if strings.Join(delivered, ",") != "compose,job-open" {
		t.Fatalf("delivered kicks = %v, want the older compose kick and job-open's open request", delivered)
	}
	if id, ok := mgr.Runner.Coordinator.Kicks().PeekPendingKickID(sess.ID); ok {
		t.Fatalf("kick %q still queued after the turn", id)
	}
}
