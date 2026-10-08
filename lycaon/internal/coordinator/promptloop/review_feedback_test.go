package promptloop

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReviewFeedbackCanReadReviseAndVerifyBeforeParking(t *testing.T) {
	reg := tools.NewStubRegistry()
	var observations []string
	for _, name := range []string{"read", "edit"} {
		testutil.FailErr(t, "register review tool", reg.Register(name, func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
			observations = append(observations, args["step"].(string))
			return "ok", nil
		}))
	}
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Tools:                 reg,
		AppendMessages:        func(context.Context, string, ...api.Message) error { return nil },
		HumanApprovalAwaiting: func(context.Context, string) bool { return true },
	})
	sess := &api.Session{ID: "review-feedback", Posture: api.SessionPostureSpec}
	var history []api.Message
	for i, tool := range []string{"read", "edit", "read"} {
		id := fmt.Sprintf("review-%d", i)
		calls := []api.ToolCall{{ID: id + "-call", Name: tool, Args: map[string]any{"path": "blueprint.md", "step": id}}}
		history = append(history, api.Message{ID: id, Role: api.MessageRoleAssistant, ToolCalls: calls})
		next, _, _, _, _, parked, err := toolBatch{loop}.executeToolCallsInTurn(t.Context(), sess, sess.ID, calls, tools.ToolContext{}, history, "Revise the blueprint", id, "", nil)
		testutil.FailErr(t, "execute review feedback", err)
		if parked {
			t.Fatalf("existing approval stopped feedback after %s", tool)
		}
		history = next
	}
	if len(observations) != 3 {
		t.Fatalf("completed review observations = %v", observations)
	}
}

func TestApprovalBoundaryAcrossSerialAndParallelBatches(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, initiallyAwaiting := range []bool{false, true} {
			for _, hostHeld := range []bool{false, true} {
				t.Run(fmt.Sprintf("shared=%v/existing=%v/host=%v", shared, initiallyAwaiting, hostHeld), func(t *testing.T) {
					reg := tools.NewStubRegistry()
					var awaiting atomic.Bool
					awaiting.Store(initiallyAwaiting)
					var callsRun atomic.Int32
					batch := toolcontract.BatchSerial
					if shared {
						batch = toolcontract.BatchShared
					}
					testutil.FailErr(t, "register boundary tool", reg.RegisterDefinition(tools.Definition{
						Meta:     tools.ToolMeta{Name: "observe"},
						Contract: toolcontract.Contract{Owner: "test", Batch: batch},
						Handler: func(context.Context, map[string]any, tools.ToolContext) (string, error) {
							callsRun.Add(1)
							awaiting.Store(true)
							return "ok", nil
						},
					}))
					loop := NewPromptLoopForTest(PromptLoopDeps{
						Tools:                 reg,
						AppendMessages:        func(context.Context, string, ...api.Message) error { return nil },
						HumanApprovalAwaiting: func(context.Context, string) bool { return awaiting.Load() },
						HostObligationHeld:    func(context.Context, string) bool { return hostHeld },
					})
					calls := []api.ToolCall{
						{ID: "one", Name: "observe", Args: map[string]any{"path": "first.md"}},
						{ID: "two", Name: "observe", Args: map[string]any{"path": "second.md"}},
					}
					sess := &api.Session{ID: "boundary"}
					history := []api.Message{{ID: "assistant", Role: api.MessageRoleAssistant, ToolCalls: calls}}
					_, _, _, _, _, parked, err := toolBatch{loop}.executeToolCallsInTurn(t.Context(), sess, sess.ID, calls, tools.ToolContext{}, history, "feedback", "assistant", "", nil)
					testutil.FailErr(t, "execute approval boundary", err)
					wantPark := !initiallyAwaiting || hostHeld
					wantCalls := int32(2)
					if wantPark && !shared {
						wantCalls = 1
					}
					if parked != wantPark || callsRun.Load() != wantCalls {
						t.Fatalf("parked=%v calls=%d; want parked=%v calls=%d", parked, callsRun.Load(), wantPark, wantCalls)
					}
				})
			}
		}
	}
}
