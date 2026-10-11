package promptloop_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

// budgetJob is a worker job whose ceiling and budget request change between rounds.
type budgetJob struct {
	mu      sync.Mutex
	max     int
	request *api.WorkerBudgetRequest
}

func (j *budgetJob) task() *api.WorkerTask {
	j.mu.Lock()
	defer j.mu.Unlock()
	return &api.WorkerTask{ID: "job-1", ParentSessionID: "parent-1", Status: api.WorkerStatusRunning, MaxToolLoops: j.max, BudgetRequest: j.request}
}

func (j *budgetJob) ask() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.request = &api.WorkerBudgetRequest{Rounds: 2, RequestedMax: j.max + 2, RemainingWork: []string{"finish the trace"}, RequestedAt: time.Now().UTC()}
}

func (j *budgetJob) grant() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.max, j.request = j.request.RequestedMax, nil
}

func (j *budgetJob) decline() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.request = nil
}

type budgetHoldRun struct {
	assistants int
	raised     int
	declined   int
	elapsed    time.Duration
}

// runBudgetHold runs a two-round worker that asks for more rounds after its
// first round; answer runs while the final round waits.
func runBudgetHold(t *testing.T, wait time.Duration, answer func(*budgetJob)) budgetHoldRun {
	t.Helper()
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".*", AlwaysTool: true,
		ToolCalls: []llm.MockToolCall{{ID: "tc1", Name: "read", Args: map[string]any{"path": "x"}}},
	}}})
	mem := store.NewMemory()
	job := &budgetJob{max: 2}
	var run budgetHoldRun
	deps := promptloop.StoreDeps(mem)
	deps.Context.Limits = func(_ context.Context, sess *api.Session) settings.SessionLimits {
		return sessionlimits.ApplyWorkerMaxToolLoops(settings.DefaultSessionLimits(), sess)
	}
	deps.Model.LLM = client
	deps.Context.Tools = tools.NewStubRegistry()
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Nudges.WorkerBudgetAnswerWait = wait
	deps.Nudges.WorkerJob = func(context.Context, string) (*api.WorkerTask, bool) { return job.task(), true }
	var asked sync.Once
	deps.Nudges.PublishWorkerProgress = func(_ context.Context, _ string, snap workerprogress.Snapshot, _ bool) {
		if snap.ToolLoopsUsed != 1 {
			return
		}
		asked.Do(func() {
			job.ask()
			if answer != nil {
				time.AfterFunc(100*time.Millisecond, func() { answer(job) })
			}
		})
	}
	deps.Nudges.WorkerBudgetRaisedNudge = func(context.Context, *api.Session, int, int) promptloop.HostNudge {
		run.raised++
		return promptloop.HostNudge{Content: "ceiling raised", SignalID: "worker.budget.raised"}
	}
	deps.Nudges.WorkerBudgetDeclinedNudge = func(context.Context, *api.Session, int, int) promptloop.HostNudge {
		run.declined++
		return promptloop.HostNudge{Content: "request declined", SignalID: "worker.budget.declined"}
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	sess.AgentType = orchestration.ProfilePathExplorer
	sess.ParentSessionID = "parent-1"
	sess.MaxToolLoops = 2
	started := time.Now()
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess, History: userHistory("go"), ProfileID: "explore_readonly",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID,
				WorkerJobID: "job-1"},
		},
	})
	run.elapsed = time.Since(started)
	testutil.FailErr(t, "loop.Run failed", err)
	msgs, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleAssistant {
			run.assistants++
		}
	}
	return run
}

func TestFinalRoundWaitsForAGrantAndContinues(t *testing.T) {
	run := runBudgetHold(t, time.Minute, (*budgetJob).grant)
	if run.assistants != 4 || run.raised != 1 || run.declined != 0 {
		t.Fatalf("run = %+v, want four rounds after the grant raised the ceiling to 4 and one raised notice", run)
	}
}

func TestFinalRoundWaitsForADeclineAndReportsIt(t *testing.T) {
	run := runBudgetHold(t, time.Minute, (*budgetJob).decline)
	if run.assistants != 2 || run.declined != 1 || run.raised != 0 {
		t.Fatalf("run = %+v, want the two-round ceiling kept and one declined notice", run)
	}
	if run.elapsed >= time.Minute {
		t.Fatalf("elapsed = %v, want the decline to end the wait", run.elapsed)
	}
}

func TestUnansweredRequestHoldsTheFinalRoundOnceForItsBound(t *testing.T) {
	const wait = 300 * time.Millisecond
	run := runBudgetHold(t, wait, nil)
	if run.assistants != 2 || run.raised != 0 || run.declined != 0 {
		t.Fatalf("run = %+v, want the final round to run at the ceiling with no answer notice", run)
	}
	if run.elapsed < wait {
		t.Fatalf("elapsed = %v, want the final round held for %v", run.elapsed, wait)
	}
}
