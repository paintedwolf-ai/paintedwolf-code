package promptloop

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolpresentation"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// HeldCallRunner supervises a call that may outlive its foreground wait.
type HeldCallRunner interface {
	Run(ctx context.Context, spec heldcall.Spec, fn heldcall.Func) (heldcall.Outcome, error)
}

// heldToolCall is one detachable call, prepared and ready to run.
type heldToolCall struct {
	receipt    *api.InvocationReceipt
	contract   toolcontract.Contract
	argsDigest string
	// run executes the tool and finalizes its result under the given context.
	run func(ctx context.Context) toolInvocation
}

// runHeldToolCall runs a detachable call. A call that settles inside its
// budget returns exactly as an ordinary call does. One that does not returns a
// running result and a handle, and keeps running under the host's context.
func (l *PromptLoop) runHeldToolCall(
	ctx context.Context,
	sess *api.Session,
	tc api.ToolCall,
	toolCtx tools.ToolContext,
	held heldToolCall,
) toolInvocation {
	// The supervised call writes settled before it finishes; the caller reads it
	// only after the call settles inside the budget.
	var settled toolInvocation
	screened, _ := l.storageSafeMessage(ctx, api.Message{ToolCalls: []api.ToolCall{tc}})
	visible := messageview.RedactMessage(screened)
	title := strings.ReplaceAll(tc.Name, "_", " ")
	if detail := toolpresentation.Title(tc.Name, visible.ToolCalls[0].Args); detail != "" {
		title += " · " + detail
	}
	outcome, err := l.Deps.HeldCalls.Run(ctx, heldcall.Spec{
		DisplayTitle: title,
		ProjectID:    sess.ProjectID, SessionID: sess.ID, ToolCallID: tc.ID, Tool: tc.Name,
		ArgsDigest: held.argsDigest, Budget: l.heldCallBudget(),
	}, func(runCtx context.Context) heldcall.Settled {
		settled = held.run(runCtx)
		return heldSettled(tc.Name, settled)
	})
	if err != nil {
		return l.refuseHeldCall(ctx, sess, tc, toolCtx, held, err)
	}
	if outcome.Settled != nil {
		return settled
	}
	return heldRunning(tc.Name, outcome, held)
}

func (l *PromptLoop) heldCallBudget() time.Duration {
	if l.Deps.HeldCallBudget > 0 {
		return l.Deps.HeldCallBudget
	}
	return heldcall.DefaultBudget
}

func heldSettled(tool string, run toolInvocation) heldcall.Settled {
	return heldcall.Settled{Tool: tool, Content: run.content, Outcome: run.facts.Resolution(), Facts: run.facts}
}

// heldRunning is the result a call returns when it outlives its wait.
func heldRunning(tool string, outcome heldcall.Outcome, held heldToolCall) toolInvocation {
	encoded, err := surveyjson.Marshal(heldcall.Result{
		Running: true, Handle: outcome.Handle, Tool: tool, WaitedMs: outcome.Waited.Milliseconds(),
	})
	if err != nil {
		return failedInvocation(err.Error(), toolCaptures{})
	}
	return toolInvocation{
		content:  string(encoded),
		facts:    guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeCompleted},
		invoked:  true,
		receipt:  held.receipt,
		contract: held.contract,
		captures: toolCaptures{
			ownerRef: outcome.Handle,
			process:  &api.ToolProcessHandle{Handle: outcome.Handle, Running: true},
		},
	}
}

// refuseHeldCall renders a duplicate or over-capacity call as a host refusal.
func (l *PromptLoop) refuseHeldCall(
	ctx context.Context,
	sess *api.Session,
	tc api.ToolCall,
	toolCtx tools.ToolContext,
	held heldToolCall,
	err error,
) toolInvocation {
	var duplicate *heldcall.DuplicateError
	var capacity *heldcall.CapacityError
	var code string
	var data map[string]any
	switch {
	case errors.As(err, &duplicate):
		code, data = "HELD_CALL_DUPLICATE_RUNNING", map[string]any{"handle": duplicate.Handle}
	case errors.As(err, &capacity):
		code, data = "HELD_CALL_CAP_REACHED", map[string]any{
			"limit": capacity.Limit, "handles": capacity.Handles, "handles_text": strings.Join(capacity.Handles, ", "),
		}
	default:
		refused := failedInvocation(err.Error(), toolCaptures{})
		refused.receipt, refused.contract = held.receipt, held.contract
		return refused
	}
	refused := refusedInvocation(l.rejectToolOccurrence(ctx, sess, tc, toolCtx, code, data))
	refused.receipt, refused.contract = held.receipt, held.contract
	return refused
}
