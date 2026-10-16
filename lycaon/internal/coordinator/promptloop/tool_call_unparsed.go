package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

// maxToolCallUnparsedRecoveries bounds consecutive repairs of native calls the
// provider dropped as invalid JSON; the next drop fails with the provider error.
const maxToolCallUnparsedRecoveries = 2

// recoverUnparsedToolCall settles a dropped native call as the response the
// model streamed before it, without tool calls, and holds TOOL_CALL_UNPARSED
// for the rejected-draft repair lifecycle. The provider error names no tool, so
// nothing is executed; any other error, or one past the bound, stays terminal.
func (l *modelTurn) recoverUnparsedToolCall(
	ctx context.Context,
	sess *api.Session,
	st *promptLoopTurnState,
	req modelcall.CompletionRequest,
	partial *modelcall.Completion,
	cause error,
	proseFinish bool,
) (*modelcall.Completion, error) {
	unparsed, ok := failure.AsProviderToolCallUnparsed(cause)
	if !ok || st == nil || proseFinish || req.ToolProtocol != modelcall.ToolProtocolNative ||
		!req.ToolsCallable() || req.ResponseFormat != nil ||
		st.output.droppedToolCallStreak >= maxToolCallUnparsedRecoveries {
		return nil, cause
	}
	completion := &modelcall.Completion{ProviderID: unparsed.ProviderID, Model: unparsed.Model}
	if partial != nil {
		completion.Content, completion.Reasoning, completion.ReasoningDetails = partial.Content, partial.Reasoning, partial.ReasoningDetails
	}
	reject, blocked, _, _ := l.Closeout.evaluateContentAnchor(ctx, sess, oar.AnchorContentOutput, oar.ContentObservation{
		Segments:         oarModelOutputSegments(completion.Content),
		ToolCallUnparsed: true,
	}, "", nil)
	if !blocked || reject == nil {
		return nil, cause
	}
	if reject.Code() != "TOOL_CALL_UNPARSED" {
		return nil, reject
	}
	st.output.droppedToolCallStreak++
	st.output.rejection = reject
	return completion, nil
}
