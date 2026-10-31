package session_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/hostcmd"
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
	mgr := session.NewHost(st, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))
	request := &api.WorkerBudgetRequest{Rounds: 4, RequestedMax: 24, RemainingWork: []string{"trace the alternate callers"}, ToolLoopsUsed: 15, RequestedAt: time.Now().UTC()}
	mgr.SetWorkerQueue(budgetJobs{
		"job-open":     {ID: "job-open", Status: api.WorkerStatusRunning, MaxToolLoops: 20, BudgetRequest: request},
		"job-answered": {ID: "job-answered", Status: api.WorkerStatusRunning, MaxToolLoops: 24},
	})

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	mgr.Coordinator.Guidance.Emit(ctx, sess.ID, anchor.ComposeDone, anchor.Envelope{})
	for _, job := range []string{"job-answered", "job-open"} {
		facts := kick.WorkerBudgetFacts{JobID: job, Used: 15, Max: 20, HostMax: 120, Request: request}
		mgr.Coordinator.Guidance.Emit(ctx, sess.ID, anchor.WorkerBudgetRequested, anchor.Envelope{Subject: job, WorkerBudget: &facts})
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

func TestTwoUserStoppedBackgroundCommandsBetweenTurnsDeliverBothToNextTurn(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	st := store.NewMemory()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewHost(st, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	now := time.Now().UTC()
	mgr.Processes.HandleCommandCompletion(ctx, bgprocess.Completion{
		SessionID:         sess.ID,
		Handle:            "bg-1",
		OriginTool:        "command",
		TerminationReason: bgprocess.TerminationStoppedByUser,
		ExitCode:          -1,
		FinishedAt:        now,
		Stages:            []hostcmd.StageResult{{Command: "npm run dev"}},
	})
	mgr.Processes.HandleCommandCompletion(ctx, bgprocess.Completion{
		SessionID:         sess.ID,
		Handle:            "bg-2",
		OriginTool:        "command",
		TerminationReason: bgprocess.TerminationStoppedByUser,
		ExitCode:          -1,
		FinishedAt:        now,
		Stages:            []hostcmd.StageResult{{Command: "tail -f app.log"}},
	})

	_, err = mgr.Submissions.Prompt(ctx, sess.ID, "status")
	testutil.FailErr(t, "prompt", err)

	var combined strings.Builder
	for _, msg := range rec.LastRequest().Messages {
		combined.WriteString(msg.Content)
		combined.WriteByte('\n')
	}
	got := combined.String()
	for _, want := range []string{
		"handle=bg-1",
		"command=npm run dev",
		"handle=bg-2",
		"command=tail -f app.log",
		"termination=stopped_by_user",
		"do not restart a `stopped_by_user` command unless the user asks",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("next turn messages missing %q:\n%s", want, got)
		}
	}
}

func TestAbortPreservesUnconsumedProcessFinishedKickWithoutAutoStartingTurn(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	st := store.NewMemory()
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}}))
	mgr := session.NewHost(st, session.Models{Client: rec, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	oartest.InstallCloseoutPolicy(t, mgr)
	wirePromptTestManager(t, mgr)
	testutil.FailErr(t, "install anchor registry", mgr.Coordinator.Guidance.InstallAnchorRegistry())
	mgr.SetPromptEngine(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}))

	ctx := context.Background()
	sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "set busy", st.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))

	mgr.Coordinator.Guidance.Emit(ctx, sess.ID, anchor.ComposeDone, anchor.Envelope{})
	mgr.Processes.HandleCommandCompletion(ctx, bgprocess.Completion{
		SessionID:         sess.ID,
		Handle:            "bg-9",
		OriginTool:        "command",
		TerminationReason: bgprocess.TerminationStoppedByUser,
		ExitCode:          -1,
		FinishedAt:        time.Now().UTC(),
		Stages:            []hostcmd.StageResult{{Command: "python3 -m http.server 8080"}},
	})

	testutil.FailErr(t, "abort turn", mgr.Stops.Abort(ctx, sess.ID, "user stopped"))
	if got := len(rec.AllRequests()); got != 0 {
		t.Fatalf("abort auto-started %d LLM requests, want 0", got)
	}
	afterAbort, err := st.Get(ctx, sess.ID)
	testutil.FailErr(t, "get session after abort", err)
	if afterAbort.Status != api.SessionStatusIdle {
		t.Fatalf("session status after abort = %q, want idle", afterAbort.Status)
	}
	if id, ok := mgr.Runner.Coordinator.Kicks().PeekPendingKickID(sess.ID); !ok || id != "coordinator-process-finished" {
		t.Fatalf("pending kick after abort = (%q, %v), want coordinator-process-finished", id, ok)
	}

	_, err = mgr.Submissions.Prompt(ctx, sess.ID, "continue")
	testutil.FailErr(t, "prompt after abort", err)
	var combined strings.Builder
	for _, msg := range rec.LastRequest().Messages {
		combined.WriteString(msg.Content)
		combined.WriteByte('\n')
	}
	got := combined.String()
	if strings.Contains(got, "Compose succeeded") {
		t.Fatalf("aborted turn's compose kick leaked into next turn:\n%s", got)
	}
	for _, want := range []string{"handle=bg-9", "command=python3 -m http.server 8080", "termination=stopped_by_user"} {
		if !strings.Contains(got, want) {
			t.Fatalf("next turn after abort missing %q:\n%s", want, got)
		}
	}
}

