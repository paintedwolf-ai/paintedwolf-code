package capabilitydrift

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/capabilitydrift/fixture"
	"github.com/lycaon/lycaon/test/contract/internal/rejectinventory"
)

// revisedDraft is an owner that starts, then finds its resource changed.
var revisedDraft = tools.Definition{
	Meta: tools.ToolMeta{Name: "publish_draft", Description: "Publish a draft.", ArgsSchema: map[string]any{
		"type": "object", "properties": map[string]any{"draft": map[string]any{"type": "string"}}, "required": []any{"draft"},
	}},
	Contract: toolcontract.Contract{Owner: "fixture", Lifecycle: toolcontract.LifecycleEffectAttempt},
	Handler: func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "", &toolrejection.ToolReject{Code: "COMMAND_REVISION_CONFLICT", Data: map[string]any{"tool": "publish_draft"}}
	},
}

func toolResultCode(t *testing.T, res turnResult, callID string) string {
	t.Helper()
	if len(res.requests) < 2 {
		t.Fatalf("no follow-up request carried the result of %s", callID)
	}
	for _, msg := range res.requests[1].Messages {
		if msg.ToolResult != nil && msg.ToolResult.ToolCallID == callID {
			return msg.ToolResult.PrimaryCode()
		}
	}
	t.Fatalf("follow-up request has no result for %s", callID)
	return ""
}

func receiptWitness(state fixture.HostState, r api.InvocationReceipt) rejectinventory.Witness {
	w := rejectinventory.Witness{Tool: r.Tool, Recipient: state.Recipient, State: state.ID, Status: string(r.Status), Invoked: r.Invoked}
	if r.Failure != nil {
		w.Code, w.FailureClass = r.Failure.Code, r.Failure.Class
	}
	if r.Isolation != nil {
		w.Disposition = r.Isolation.Disposition
	}
	return w
}

// Availability is compared at the stage that decides it, on the request that
// carried the offer: missing reach selects no owner and opens no receipt; a
// selected call can be refused before its owner runs; an owner that started
// may revalidate and refuse; and only a request-time admission denial
// contradicts the offer.
func TestAvailabilityComparedAtItsAdmissionStage(t *testing.T) {
	t.Run("missing_reach_selects_no_owner", func(t *testing.T) {
		res := runTurn(t, turn{state: restricted, projection: fixture.AdmittedProjection, calls: []api.ToolCall{shareCall}})
		if offers(res.requests[0], fixture.ToolName) {
			t.Fatal("restricted request offers the action")
		}
		if len(res.begun) != 0 {
			t.Errorf("an unreached call opened receipts: %+v", res.begun)
		}
		if code := toolResultCode(t, res, shareCall.ID); code != "TOOL_NOT_OFFERED" {
			t.Errorf("unreached call result code = %q, want TOOL_NOT_OFFERED", code)
		}
	})

	t.Run("pre_owner_rejection_settles_the_selected_receipt", func(t *testing.T) {
		bad := api.ToolCall{ID: "call-bad", Name: fixture.ToolName, Args: map[string]any{"report": "weekly", "audience": "everyone"}}
		res := runTurn(t, turn{state: admitted, projection: fixture.AdmittedProjection, calls: []api.ToolCall{bad}})
		if len(res.receipts) != 1 {
			t.Fatalf("receipts = %+v, want the selected call's one receipt", res.receipts)
		}
		r := res.receipts[0]
		if r.Status != api.InvocationStatusRejected || r.Invoked || r.Failure == nil {
			t.Errorf("pre-owner rejection receipt = %+v, want rejected and not invoked", r)
		}
		if AdmissionDenial(receiptWitness(admitted, r)) {
			t.Errorf("argument rejection %s withdrew availability", r.Failure.Code)
		}
	})

	t.Run("owner_revalidation_after_start", func(t *testing.T) {
		call := api.ToolCall{ID: "call-draft", Name: revisedDraft.Meta.Name, Args: map[string]any{"draft": "q3"}}
		res := runTurn(t, turn{state: admitted, projection: fixture.AdmittedProjection, calls: []api.ToolCall{call}, extra: []tools.Definition{revisedDraft}})
		if len(res.receipts) != 1 {
			t.Fatalf("receipts = %+v, want one", res.receipts)
		}
		r := res.receipts[0]
		if !r.Invoked || r.Failure == nil || r.Failure.Code != "COMMAND_REVISION_CONFLICT" {
			t.Errorf("revalidation receipt = %+v, want an invoked refusal with the owner's code", r)
		}
		if AdmissionDenial(receiptWitness(admitted, r)) {
			t.Error("a changed resource at effect time withdrew an earlier offer")
		}
	})

	t.Run("request_time_owner_denial", func(t *testing.T) {
		res := runTurn(t, turn{state: restricted, projection: fixture.StaleProjection, calls: []api.ToolCall{shareCall}})
		if len(res.receipts) != 1 {
			t.Fatalf("receipts = %+v, want one", res.receipts)
		}
		r := res.receipts[0]
		if r.Invoked || r.Failure == nil || r.Failure.Code != fixture.RefusalCode || r.Failure.Class != api.FailureClassPolicyRejection {
			t.Errorf("owner denial receipt = %+v, want a policy refusal before effect", r)
		}
		if !AdmissionDenial(receiptWitness(restricted, r)) {
			t.Error("a request-time owner denial did not withdraw availability")
		}
	})
}
