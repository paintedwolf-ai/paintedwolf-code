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

func TestExecuteToolCallsInTurnCommitsAssistantBeforeFirstTool(t *testing.T) {
	var commitBeforeAppend bool
	reg := tools.NewStubRegistry()
	_ = reg.Register("answer_decision", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return `{"status":"resumed"}`, nil
	})

	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Tools: reg,
		},
	})
	sess := &api.Session{ID: "sess-1", Posture: api.SessionPostureBuild}
	assistantID := "asst-1"
	history := []api.Message{{
		ID:         assistantID,
		Role:       api.MessageRoleAssistant,
		Visibility: api.MessageVisibilityInternal,
		Content:    "Two workers need decisions.",
		ToolCalls: []api.ToolCall{
			{ID: "tc1", Name: "answer_decision", Args: map[string]any{"job_id": "job-1", "option": "1"}},
		},
	}}

	loop.Projection.Deps.UpdateMessage = func(_ context.Context, _, _ string, msg api.Message) error {
		if msg.Visibility == api.MessageVisibilityTranscript {
			commitBeforeAppend = true
		}
		for i := range history {
			if history[i].ID == msg.ID {
				history[i] = msg
			}
		}
		return nil
	}
	var appended []api.Message
	loop.Projection.Deps.AppendMessages = func(_ context.Context, _ string, msgs ...api.Message) error {
		if !commitBeforeAppend {
			t.Fatal("assistant must commit to transcript before tool result append")
		}
		appended = append(appended, msgs...)
		return nil
	}

	_, turnTools, _, _, _, _, err := loop.Batch.executeToolCallsInTurn(
		context.Background(),
		sess,
		sess.ID,
		history[0].ToolCalls,
		tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
		history,
		"dispatch",
		assistantID,
		"",
		nil,
	)
	testutil.FailErr(t, "executeToolCallsInTurn", err)
	if len(turnTools) != 1 || turnTools[0] != "answer_decision" {
		t.Fatalf("turnTools = %v want [answer_decision]", turnTools)
	}
	if len(appended) != 1 || appended[0].Role != api.MessageRoleTool {
		t.Fatalf("appended = %+v want one tool row", appended)
	}
	if history[0].Visibility != api.MessageVisibilityTranscript {
		t.Fatalf("assistant visibility = %q want transcript", history[0].Visibility)
	}
}

func TestIterationRunwayFires(t *testing.T) {
	coordinator := &api.Session{ID: "coordinator"}
	worker := &api.Session{ID: "worker", ParentSessionID: "coordinator"}
	cases := []struct {
		name      string
		sess      *api.Session
		iterIndex int
		maxIter   int
		want      bool
	}{
		{"coordinator budget at threshold", coordinator, 290, 300, true},
		{"coordinator budget before threshold", coordinator, 289, 300, false},
		{"coordinator small budget never fires", coordinator, 9, 19, false},
		{"coordinator budget exactly twice threshold", coordinator, 10, 20, true},
		{"worker runway is a third of its ceiling", worker, 13, 20, true},
		{"worker before its runway", worker, 12, 20, false},
		{"worker runway stops at ten rounds", worker, 30, 40, true},
		{"worker runway is at least three rounds", worker, 1, 4, true},
		{"worker with one round has no runway", worker, 0, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := iterationRunwayFires(tc.sess, tc.iterIndex, tc.maxIter); got != tc.want {
				t.Fatalf("iterationRunwayFires(%s, %d, %d) = %v want %v", tc.sess.ID, tc.iterIndex, tc.maxIter, got, tc.want)
			}
		})
	}
}

func toolResultRow(assistantID, callID string) api.Message {
	return api.Message{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			AssistantMessageID: assistantID,
			ToolCallID:         callID,
			Outcome:            api.ToolResultOutcomeRejected,
		},
	}
}

func callIDs(calls []api.ToolCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.ID)
	}
	return out
}

func TestAnsweredToolCallsDropsCallsThatNeverRan(t *testing.T) {
	calls := []api.ToolCall{{ID: "call-1", Name: "grep"}}
	got := answeredToolCalls([]api.Message{}, "assistant-1", calls)
	if len(got) != 0 {
		t.Fatalf("answeredToolCalls = %v, want none: an unanswered call must not stay on a tools-omitted turn", callIDs(got))
	}
}

// TestAnsweredToolCallsKeepsAnsweredCalls keeps the call a receipt already names.
func TestAnsweredToolCallsKeepsAnsweredCalls(t *testing.T) {
	calls := []api.ToolCall{{ID: "call-1", Name: "grep"}}
	history := []api.Message{toolResultRow("assistant-1", "call-1")}

	got := answeredToolCalls(history, "assistant-1", calls)
	if len(got) != 1 || got[0].ID != "call-1" {
		t.Fatalf("answeredToolCalls = %v, want [call-1]: dropping it strands the receipt", callIDs(got))
	}
}

func TestAnsweredToolCallsKeepsOnlyTheAnsweredOnes(t *testing.T) {
	calls := []api.ToolCall{
		{ID: "call-ran", Name: "grep"},
		{ID: "call-never-ran", Name: "read"},
	}
	history := []api.Message{toolResultRow("assistant-1", "call-ran")}

	got := answeredToolCalls(history, "assistant-1", calls)
	if len(got) != 1 || got[0].ID != "call-ran" {
		t.Fatalf("answeredToolCalls = %v, want [call-ran] only", callIDs(got))
	}
}

// TestAnsweredToolCallsIgnoresOtherRowsReceipts ignores receipts from other rows.
func TestAnsweredToolCallsIgnoresOtherRowsReceipts(t *testing.T) {
	calls := []api.ToolCall{{ID: "call-1", Name: "grep"}}
	history := []api.Message{toolResultRow("assistant-other", "call-1")}

	got := answeredToolCalls(history, "assistant-1", calls)
	if len(got) != 0 {
		t.Fatalf("answeredToolCalls = %v, want none: the receipt names another row", callIDs(got))
	}
}

func TestAnsweredToolCallsWithoutAnAssistantRowKeepsNothing(t *testing.T) {
	calls := []api.ToolCall{{ID: "call-1", Name: "grep"}}
	history := []api.Message{toolResultRow("", "call-1")}

	got := answeredToolCalls(history, "", calls)
	if len(got) != 0 {
		t.Fatalf("answeredToolCalls = %v, want none: no row means no pair to preserve", callIDs(got))
	}
}

// TestAnsweredToolCallsNeverReturnsNil keeps the storage projection total: the
// assistant row's batch is an empty array, never absent.
func TestAnsweredToolCallsNeverReturnsNil(t *testing.T) {
	if got := answeredToolCalls(nil, "assistant-1", nil); got == nil {
		t.Fatal("answeredToolCalls returned nil; the batch must always be an array")
	}
}

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
				Context: ContextDeps{
					Tools: reg,
				},
				Tools: ToolsDeps{
					BeforeToolRun: func(_ context.Context, _ *api.Session, _ []api.Message, _ string, _ string, args map[string]any) (string, bool, error) {
						index := args["index"].(int)
						inspected = append(inspected, index)
						if rejects[index] {
							return "", false, guidance.NewRefusal("TOOL_ARGS_INVALID", fmt.Sprintf("Invalid brief %d", index))
						}
						return "", false, nil
					},
				},
				Projection: ProjectionDeps{
					AppendMessages: func(_ context.Context, _ string, messages ...api.Message) error {
						stored = append(stored, messages...)
						return nil
					},
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
			history, _, dispatched, count, _, stopped, err := loop.Batch.executeToolCallsInTurn(t.Context(), sess, sess.ID, calls,
				tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID}}, history, "build", "assistant", "", state)
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
