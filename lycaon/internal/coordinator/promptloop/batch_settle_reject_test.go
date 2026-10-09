package promptloop

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDispatchRejectionsPreserveIndependentPeersAndHistory(t *testing.T) {
	for _, rejected := range [][]int{{0}, {1}, {0, 2, 5}} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			reg := tools.NewStubRegistry()
			var ran []int
			testutil.FailErr(t, "register task", reg.Register("task", func(_ context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
				index := args["index"].(int)
				ran = append(ran, index)
				tctx.Effects.Out.Dispatch = &api.WorkerDispatch{WorkerID: fmt.Sprint(index)}
				return "queued", nil
			}))
			rejects := map[int]bool{}
			for _, index := range rejected {
				rejects[index] = true
			}
			var stored []api.Message
			var inspected []int
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Tools: reg,
				BeforeToolRun: func(_ context.Context, _ *api.Session, _ []api.Message, _ string, _ string, args map[string]any) (string, bool, error) {
					index := args["index"].(int)
					inspected = append(inspected, index)
					if rejects[index] {
						return "", false, guidance.NewRefusal("TOOL_ARGS_INVALID", fmt.Sprintf("Invalid brief %d", index))
					}
					return "", false, nil
				},
				AppendMessages: func(_ context.Context, _ string, messages ...api.Message) error {
					stored = append(stored, messages...)
					return nil
				},
			})
			calls := make([]api.ToolCall, 6)
			for i := range calls {
				args := taskCallArgs("implementer", fmt.Sprint(i))
				args["index"] = i
				calls[i] = api.ToolCall{ID: fmt.Sprint("call-", i), Name: "task", Args: args}
			}
			history := []api.Message{{ID: "assistant", Role: api.MessageRoleAssistant, ToolCalls: calls, CreatedAt: time.Now().UTC()}}
			state := &promptLoopTurnState{}
			sess := &api.Session{ID: "session", Posture: api.SessionPostureBuild}
			history, _, dispatched, count, _, stopped, err := toolBatch{loop}.executeToolCallsInTurn(t.Context(), sess, sess.ID, calls,
				tools.ToolContext{
					Identity: tools.InvocationIdentity{SessionID: sess.ID},
				}, history, "build", "assistant", "", state)
			testutil.FailErr(t, "dispatch wave", err)
			if stopped || !dispatched || count != 6-len(rejected) || len(ran) != count || len(inspected) != 6 {
				t.Fatalf("stopped=%v dispatched=%v count=%d ran=%v inspected=%v", stopped, dispatched, count, ran, inspected)
			}
			if len(history) != 7 || history[0].ID != "assistant" || len(history[0].ToolCalls) != 6 {
				t.Fatalf("dispatch history lost call/result context: %+v", history)
			}
			for i, message := range history[1:] {
				result := message.ToolResult
				if result == nil || result.ToolCallID != calls[i].ID || result.AssistantMessageID != "assistant" {
					t.Fatalf("result %d has incorrect pairing: %+v", i, result)
				}
				if rejects[i] {
					if result.Outcome != api.ToolResultOutcomeRejected || result.Content != fmt.Sprintf("Invalid brief %d", i) {
						t.Fatalf("result %d lost its own rejection: %+v", i, result)
					}
				} else if result.Outcome != api.ToolResultOutcomeCompleted || result.Dispatch == nil {
					t.Fatalf("accepted peer %d lost dispatch receipt: %+v", i, result)
				}
			}
			if len(stored) < 6 {
				t.Fatalf("only %d durable result rows", len(stored))
			}
		})
	}
}
