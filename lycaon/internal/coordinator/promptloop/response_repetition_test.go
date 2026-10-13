package promptloop_test

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBatchRejectionsEscalateOnlyAcrossResponses(t *testing.T) {
	for _, beforeInvoke := range []bool{true, false} {
		name := "invoked"
		if beforeInvoke {
			name = "pre-invoke"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			guard := loopguard.NewMemoryDoomLoopGuard()
			messages := store.NewMemory()
			sess, err := messages.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session", err)
			client := &varyingArgsToolClient{batchSize: 5, stopAfter: 3}
			deps := promptloop.StoreDeps(messages)
			deps.Model.LLM, deps.Context.Tools, deps.Context.Policy, deps.Nudges.DoomLoop = client, tools.NewStubRegistry(), &recordingToolPolicy{}, guard
			refusal := func() error {
				return guidance.NewRefusal("TOOL_NOT_OFFERED", "schema not loaded").WithCause(&toolrejection.ToolReject{Code: "TOOL_NOT_OFFERED", FailureClass: api.FailureClassPolicyRejection})
			}
			if beforeInvoke {
				deps.Tools.BeforeToolRun = func(context.Context, *api.Session, []api.Message, string, string, map[string]any) (string, bool, error) {
					return "", false, refusal()
				}
			} else {
				testutil.FailErr(t, "register rejecting tool", deps.Context.Tools.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) { return "", refusal() }))
			}
			var mu sync.Mutex
			var counts []int
			deps.Nudges.EscalateRepeatedCode = func(_ context.Context, sessionID, tool string, original *guidance.Refusal) *guidance.Refusal {
				count := guard.CodeRejectResponses(sessionID, tool, original.Code())
				mu.Lock()
				counts = append(counts, count)
				mu.Unlock()
				return nil
			}
			_, err = promptloop.NewPromptLoopForTest(deps).Run(ctx, promptloop.PromptRunInput{SessionID: sess.ID, Session: sess, History: userHistory("inspect portraits"), ProfileID: "coordinator", ToolCtx: tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID}}})
			testutil.FailErr(t, "run batched rejections", err)
			if len(counts) != 15 {
				t.Fatalf("recorded counts = %v, want 15 rejected calls", counts)
			}
			for i, count := range counts {
				if want := i/5 + 1; count != want {
					t.Fatalf("call %d: responses = %d, want %d", i, count, want)
				}
			}
		})
	}
}

func TestOfferedSchemaResolvesEarlierLoadingRejections(t *testing.T) {
	ctx := t.Context()
	guard := loopguard.NewMemoryDoomLoopGuard()
	messages := store.NewMemory()
	sess, err := messages.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	args := map[string]any{"path": "portrait.svg"}
	for response := range loopguard.DoomLoopMaxAttempts {
		testutil.FailErr(t, "seed missing schema", guard.RecordAttempt(ctx, sess.ID, fmt.Sprint(response), "read", args, "TOOL_NOT_OFFERED", false))
	}
	deps := promptloop.StoreDeps(messages)
	deps.Context.Tools, deps.Context.Policy, deps.Nudges.DoomLoop = tools.NewStubRegistry(), &recordingToolPolicy{}, guard
	invoked := 0
	testutil.FailErr(t, "register loaded tool", deps.Context.Tools.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		invoked++
		return "SVG source", nil
	}))
	deps.Model.LLM = llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".*", FollowUpText: "Inspected.", ToolCalls: []llm.MockToolCall{{ID: "read-svg", Name: "read", Args: args}}}}})
	_, err = promptloop.NewPromptLoopForTest(deps).Run(ctx, promptloop.PromptRunInput{SessionID: sess.ID, Session: sess, History: userHistory("inspect"), ProfileID: "coordinator", ToolCtx: tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID}}})
	testutil.FailErr(t, "run after loading", err)
	if invoked != 1 {
		t.Fatalf("loaded tool invoked %d times, want once", invoked)
	}
}
