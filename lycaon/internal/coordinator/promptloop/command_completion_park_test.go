package promptloop_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromptLoopCommandUntilCompleteParksWithZeroIntermediateModelDispatches(t *testing.T) {
	storage := storetest.New(t, testdbseed.DefaultProjectID)
	sess, err := storage.Sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	pf := programtest.New(t, programtest.Options{})
	testdbseed.InsertSession(t, pf.Database, sess.ID, testdbseed.DefaultProjectID)
	pf.Runtime.Sessions = sessionOwnersStub{sess: sess}
	pf.Session = sess.ID
	clock := programtest.NewManualClock()
	pf.Runtime.Clock = clock
	entered, release := make(chan struct{}), make(chan struct{})
	pf.Runtime.Evaluate = func(_ context.Context, _ commandsurface.ProgramSnapshot, ports commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		close(entered)
		<-release
		_, _ = fmt.Fprint(ports.Streams.Output, "done-from-command\n")
		return commandprogram.ProgramOutcome{Status: 0}, nil
	}

	store := &awaitstore.Store{DB: pf.Database}
	wakes := loopwake.NewLoopEngine()
	resumed := make(chan loopwake.WaitDelivery, 1)
	wakes.SetDeps(loopwake.LoopDeps{
		GetSession:         storage.Sessions.Get,
		HostWakeActionable: func(context.Context, loopwake.HostWakeActionableInput) bool { return true },
		RunWaitResume: func(_ context.Context, _ string, delivery loopwake.WaitDelivery) (*promptresult.Result, error) {
			if err := delivery.Admitted(); err != nil {
				return nil, err
			}
			resumed <- delivery
			return &promptresult.Result{}, nil
		},
	})
	wakes.Subscriptions.SetWaitStore(store)
	t.Cleanup(func() { wakes.ForgetSession(context.Background(), sess.ID) })
	pf.Runtime.Parking = loopwake.ProgramParking{Subscriptions: wakes.Subscriptions}

	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register command", reg.Register("command", func(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
		pf.Configure = func(ptc *tools.ToolContext) {
			ptc.Effects.Out = tc.Effects.Out
			ptc.Identity.ToolCallID = tc.Identity.ToolCallID
		}
		return pf.CallArgs(t, ctx, args)
	}))

	client := &sequentialLLMClient{completions: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{{ID: "cmd-1", Name: "command", Args: map[string]any{"command": "slow-op", "until_complete": true}}}},
		{Content: "unexpected second model call"},
	}}
	deps := promptloop.StoreDeps(storage)
	deps.Context.Tools, deps.Model.LLM = reg, client
	loop := promptloop.NewPromptLoopForTest(deps)

	type loopOut struct {
		res *promptloop.PromptRunResult
		err error
	}
	done := make(chan loopOut, 1)
	go func() {
		res, err := loop.Run(t.Context(), promptloop.PromptRunInput{
			SessionID: sess.ID, Session: sess, ProfileID: "implement",
			ToolCtx: tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID}},
			History: userHistory("run slow-op"),
		})
		done <- loopOut{res, err}
	}()

	select {
	case <-entered:
	case early := <-done:
		t.Fatalf("promptloop exited before Evaluate entered: res=%+v err=%v", early.res, early.err)
	}
	clock.Advance(toolcontract.ProgramInlineWait)
	out := <-done
	testutil.FailErr(t, "promptloop run", out.err)
	if len(client.requests) != 1 {
		t.Fatalf("model dispatches = %d, want 1 (zero intermediate dispatches)", len(client.requests))
	}
	if !wakes.Waits.IsSleeping(sess.ID) {
		t.Fatal("session was not parked on wait lease")
	}

	close(release)
	delivery := <-resumed
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode outcome", json.Unmarshal([]byte(delivery.Condition.Report), &outcome))
	if delivery.Condition.Outcome != "completed" || outcome.Status != 0 || outcome.Stdout != "done-from-command\n" {
		t.Fatalf("unexpected delivery %+v outcome %+v", delivery, outcome)
	}
}

type sessionOwnersStub struct{ sess *api.Session }

func (s sessionOwnersStub) Get(context.Context, string) (*api.Session, error) {
	return s.sess, nil
}
