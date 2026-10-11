package promptloop

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/noticeerr"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// refusingRecorder refuses every settlement, as the ledger does for an
// outcome it cannot record, and answers with the host-fault receipt.
type refusingRecorder struct {
	mu    sync.Mutex
	begun []string
}

func (r *refusingRecorder) Begin(_ context.Context, in invocation.Start) (*api.InvocationReceipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.begun = append(r.begun, in.ToolCallID)
	return &api.InvocationReceipt{
		ID: uuid.NewString(), Tool: in.ToolName, ToolCallID: in.ToolCallID,
		Owner: in.Contract.Owner, Status: api.InvocationStatusRunning,
	}, nil
}

func (r *refusingRecorder) Settle(_ context.Context, id string, in invocation.Settlement) (*api.InvocationReceipt, error) {
	return nil, &invocation.SettlementRefusedError{
		ReceiptID: id,
		Receipt: &api.InvocationReceipt{
			ID: id, Status: api.InvocationStatusError, Invoked: in.Invoked,
			Evidence: api.InvocationEvidence{Kind: invocation.EvidenceKindHostFault},
			Failure:  &api.InvocationFailure{Code: invocation.SettlementRefusedCode, Class: api.FailureClassHostFault},
		},
		Violation: errors.New("fixture violation"),
	}
}

func (r *refusingRecorder) ListSession(context.Context, string) ([]api.InvocationReceipt, error) {
	return nil, nil
}

func (r *refusingRecorder) ListSessionPage(context.Context, string, string, int) (api.InvocationReceiptList, error) {
	return api.InvocationReceiptList{}, nil
}

func (r *refusingRecorder) InterruptRunning(context.Context) (int64, error) { return 0, nil }

// A refused settlement ends the turn as a host fault with the transcript whole:
// the call's result is durable and bound to its host-fault receipt, and the
// batch's remaining calls settle as not run.
func TestRefusedSettlementEndsTurnAsHostFault(t *testing.T) {
	reg := tools.NewStubRegistry()
	var ran []string
	testutil.FailErr(t, "register write", reg.Register("write", func(_ context.Context, args map[string]any, _ tools.ToolContext) (string, error) {
		path, _ := args["path"].(string)
		ran = append(ran, path)
		return "Wrote 3 bytes to " + path, nil
	}))
	var appended []api.Message
	recorder := &refusingRecorder{}
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Tools: reg,
		},
		Tools: ToolsDeps{
			Invocations: recorder,
		},
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
				appended = append(appended, msgs...)
				return nil
			},
		},
	})
	calls := []api.ToolCall{
		{ID: "first", Name: "write", Args: map[string]any{"path": "a.md", "content": "a"}},
		{ID: "second", Name: "write", Args: map[string]any{"path": "b.md", "content": "b"}},
	}
	sess := &api.Session{ID: "session"}
	history := []api.Message{{ID: "assistant", Role: api.MessageRoleAssistant, ToolCalls: calls}}

	history, _, _, _, _, stopped, err := loop.Batch.executeToolCallsInTurn(t.Context(), sess, sess.ID, calls, tools.ToolContext{}, history, "implement", "assistant", "", nil)

	var fault *HostFaultError
	if !errors.As(err, &fault) || fault.Tool != "write" || !fault.CallRan {
		t.Fatalf("turn error = %v, want a host fault for the write that ran", err)
	}
	if code, ok := noticeerr.CodeOf(err); !ok || code != api.NoticeCodeHostFault {
		t.Fatalf("turn notice code = %q, %v", code, ok)
	}
	if stopped {
		t.Fatal("a host fault is a turn failure, not a parked cycle")
	}
	if !slices.Equal(ran, []string{"a.md"}) || !slices.Equal(recorder.begun, []string{"first"}) {
		t.Fatalf("ran=%v begun=%v: the call after the fault must not run", ran, recorder.begun)
	}
	results := map[string]*api.ToolResult{}
	for _, msg := range history {
		if msg.ToolResult != nil {
			results[msg.ToolResult.ToolCallID] = msg.ToolResult
		}
	}
	first := results["first"]
	if first == nil || first.Outcome != api.ToolResultOutcomeError ||
		!strings.Contains(first.Content, invocation.SettlementRefusedCode) ||
		strings.Contains(first.Content, "Wrote 3 bytes") ||
		first.Invocation == nil || first.Invocation.Failure == nil ||
		first.Invocation.Failure.Class != api.FailureClassHostFault {
		t.Fatalf("host-fault result = %+v", first)
	}
	second := results["second"]
	if second == nil || second.Outcome != api.ToolResultOutcomeRejected ||
		!strings.Contains(second.Content, "TOOL_BATCH_NOT_RUN") {
		t.Fatalf("unrun result = %+v", second)
	}
	persisted := 0
	for _, msg := range appended {
		if msg.ToolResult != nil {
			persisted++
		}
	}
	if persisted != 2 {
		t.Fatalf("persisted %d tool results, want both calls answered", persisted)
	}
}
