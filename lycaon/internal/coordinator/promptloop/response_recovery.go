package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/responserecovery"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

// recoverRejectedResponse retains failed content without committing its calls.
func (l *modelTurn) recoverRejectedResponse(
	ctx context.Context,
	sess *api.Session,
	st *promptLoopTurnState,
	req modelcall.CompletionRequest,
	source *modelcall.SourceRequestCapture,
	partial *modelcall.Completion,
	cause error,
	remainingCalls int,
) (*modelcall.Completion, error) {
	if st == nil {
		return nil, cause
	}
	// Collection precedes dispatch of every call in this response.
	decision := st.output.recovery.Decide(responserecovery.Facts{
		Cause: cause, Commit: responserecovery.ResponseUncommitted,
		Canceled: ctx.Err() != nil, RemainingCalls: remainingCalls,
		NativeTools:      req.ToolProtocol == modelcall.ToolProtocolNative && req.ToolsCallable(),
		StructuredOutput: req.ResponseFormat != nil,
	})
	if decision.Code == "" {
		return nil, cause
	}
	completion := &modelcall.Completion{ProviderID: decision.ProviderID, Model: decision.Model}
	if partial != nil {
		completion.Content, completion.Reasoning, completion.ReasoningDetails = partial.Content, partial.Reasoning, partial.ReasoningDetails
	}
	reject, blocked, _, _ := l.Closeout.evaluateContentAnchor(ctx, sess, oar.AnchorContentOutput, oar.ContentObservation{
		Segments:           oarModelOutputSegments(completion.Content),
		ToolCallUnparsed:   decision.Code == "TOOL_CALL_UNPARSED",
		InvalidResponse:    decision.Code == "MODEL_RESPONSE_INVALID",
		ToolCallDiagnostic: decision.Diagnostic,
	}, "", nil)
	if !blocked || reject == nil {
		return nil, cause
	}
	if reject.Code() != decision.Code {
		return nil, reject
	}
	st.output.recovery.Replaced()
	st.output.rejection = reject
	completion.SourceContext = source.ForCompletion(completion)
	st.output.sourceContext = completion.SourceContext
	return completion, nil
}
