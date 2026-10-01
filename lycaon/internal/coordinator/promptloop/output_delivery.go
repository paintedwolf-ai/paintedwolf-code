package promptloop

import (
	"context"
	"maps"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Output admission happens after execution; refusing delivery does not undo it.
func (l *PromptLoop) refuseOutputDelivery(ctx context.Context, sess *api.Session, tc api.ToolCall, toolCtx tools.ToolContext, run toolInvocation, code string, data map[string]any) toolInvocation {
	data = maps.Clone(data)
	if data == nil {
		data = map[string]any{}
	}
	data["execution_outcome"] = string(run.facts.Resolution())
	if run.failure != nil {
		data["execution_failure_code"] = run.failure.Code
		data["execution_failure_class"] = run.failure.Class
	}
	run.failure = rejectionFailure(code, api.FailureClassOwnerError, invocationFailureOwner(run.contract, run.captures), data)
	return run.refusedBy(l.rejectToolOccurrence(ctx, sess, tc, toolCtx, code, data))
}
