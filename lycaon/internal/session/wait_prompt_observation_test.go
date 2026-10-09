package session

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestConsumedStartupWakeKeepsWaitingUserTurnBusy(t *testing.T) {
	ctx := t.Context()
	memory := store.NewMemory()
	client := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "unexpected follow-up"}}}))
	mgr := NewManager(memory, client, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	sess, err := memory.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "start visible turn", memory.SetSessionStatus(ctx, sess.ID, api.SessionStatusBusy))
	loopWorkflowFixture1 := &settlementWorkflowSource{active: &api.WorkflowRun{
		ID: "run", Revision: 5, Status: api.WorkflowRunStatusRunning, CurrentPhase: "work",
	}}
	mgr.SetLoopWorkflowSource(&loopwake.WorkflowDomains{Runs: loopWorkflowFixture1, Approvals: loopWorkflowFixture1, Obligations: loopWorkflowFixture1})
	loop := mgr.ensureCoordinatorRuntime().CoordinatorLoop()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), sess.ID) })
	finish := loop.BeginPromptExecution(ctx, sess.ID)
	observe := mgr.Runner.Coordinator.PromptLoop().Context.Deps.ObservePrompt(sess.ID)
	loop.Nudge(ctx, sess.ID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	observe(inject.CoordinatorTurnFrame{WorkflowRevision: 5, RunContext: api.CoordinatorRunContext{RunID: "run"}})
	loop.EnterSleep(ctx, sess.ID, time.Now().Add(time.Hour), "command", []loopwake.WaitTrigger{loopwake.WaitTriggerTimer, loopwake.WaitTriggerProcessDone}, []string{"process"}, loopwake.SleepMoverHost)
	loop.MarkWaitCalled(sess.ID)
	testutil.FailErr(t, "finish parked prompt", mgr.Runner.Settlement.Finish(ctx, sess.ID, false, false, ""))
	finish()
	loop.WaitForAsyncTurns(testutil.BoundedContext(t, 2*time.Second))
	testutil.FailErr(t, "settle drained wakes", mgr.Runner.Settlement.Drain(ctx, sess.ID))
	current, err := memory.Get(ctx, sess.ID)
	testutil.FailErr(t, "read waiting session", err)
	if current.Status != api.SessionStatusBusy || !loop.IsSleeping(sess.ID) || len(client.AllRequests()) != 0 {
		t.Fatalf("status=%s sleeping=%v prompts=%d", current.Status, loop.IsSleeping(sess.ID), len(client.AllRequests()))
	}
}
