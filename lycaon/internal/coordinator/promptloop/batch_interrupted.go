package promptloop

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// settleUnattemptedCalls records calls skipped after a cycle boundary.
func (l *toolBatch) settleUnattemptedCalls(
	ctx context.Context, sess *api.Session, sessionID, assistantID string,
	history []api.Message, calls []api.ToolCall, turnTools []string,
	lastToolTS *time.Time, st *promptLoopTurnState,
) ([]api.Message, []string, error) {
	settled := make(map[string]bool)
	for _, msg := range history {
		if result := msg.ToolResult; result != nil && result.AssistantMessageID == assistantID {
			settled[result.ToolCallID] = true
		}
	}
	for _, call := range calls {
		if settled[call.ID] {
			continue
		}
		reject := l.Tools.toolReject("TOOL_BATCH_NOT_RUN", map[string]any{"tool": call.Name})
		msg := l.Tools.toolRejectMessage(call.Name, call.ID, assistantID, call.Args, reject)
		var err error
		history, err = l.persistClassifiedToolOutcome(ctx, sessionID, sess, history,
			toolCallOutcome{toolName: call.Name, toolArgs: call.Args, toolMsg: msg}, lastToolTS, st)
		if err != nil {
			return history, turnTools, err
		}
		turnTools = l.settleToolRow(ctx, sess, sessionID, call.Name, turnTools, st)
	}
	return history, turnTools, nil
}

// settleToolRow advances cards after the result is durable.
func (l *toolBatch) settleToolRow(
	ctx context.Context,
	sess *api.Session,
	sessionID, toolName string,
	turnTools []string,
	st *promptLoopTurnState,
) []string {
	l.announceToolAskAfterCommit(ctx, sessionID, toolName)
	l.Nudges.publishWorkerProgress(ctx, sess, st.workerRunID(), st.progress().SettleCall(), false)
	return append(turnTools, toolName)
}

// attachRejectReceipt binds the settled invocation to its own tool result.
func attachRejectReceipt(out singleToolOutcome, receipt *api.InvocationReceipt) singleToolOutcome {
	if out.toolMsg.ToolResult != nil {
		out.toolMsg.ToolResult.Invocation = receipt
	}
	return out
}

func (l *toolBatch) settleToolResult(
	ctx context.Context,
	tc api.ToolCall,
	toolCtx tools.ToolContext,
	toolMsg api.Message,
	run toolInvocation,
) (toolInvocation, error) {
	status := api.InvocationStatusCompleted
	evidenceKind := string(run.contract.Evidence())
	if run.hostAnswered {
		// Host answers carry no subsystem evidence.
		evidenceKind = "host_answer"
	}
	if ctx.Err() != nil {
		status = api.InvocationStatusInterrupted
		evidenceKind = "user_stop"
		if run.failure == nil {
			run.failure = statedOrOwnerFailure(run.facts, run.contract, run.captures)
		}
	} else if !run.succeeded() {
		status = api.InvocationStatusError
		evidenceKind = "error"
		if run.failure == nil {
			run.failure = statedOrOwnerFailure(run.facts, run.contract, run.captures)
		}
	}
	ownerRef := invocationOwnerRef(run.captures.ownerRef, toolMsg.ToolResult)
	run.sourceRevision, run.sourceRootDigest = invocation.SourceRevisionForRoot(tools.HostWriteRoot(toolCtx))
	if tc.Name == "complete_leg" {
		run.sourceRevision, run.sourceRootDigest = sourceledger.VerificationState(ctx, toolCtx.Source.SourceLedger, tools.HostWriteRoot(toolCtx))
	}
	if source := run.captures.sourceRun; source != nil {
		run.sourceRevision, run.sourceRootDigest = source.SourceRevision, source.SourceRootDigest
	}
	return l.Tools.settleInvocation(ctx, run, status, evidenceKind, toolMsg.ID, ownerRef)
}

func (l *toolBatch) toolResultLimits(ctx context.Context, sess *api.Session) (int, int) {
	if l.Context.Deps.Limits == nil {
		return 0, 0
	}
	limits := l.Context.Deps.Limits(ctx, sess)
	return limits.MaxToolResultBytes, limits.MaxToolSpillBytes
}

func (l *toolBatch) preflightToolCall(
	ctx context.Context,
	sess *api.Session,
	sessionID, responseID string,
	tc api.ToolCall,
	taskAllowlist []string,
) (context.Context, int, *guidance.Refusal) {
	if taskAllowlist != nil {
		ctx = toolpolicy.WithTaskSpawnAllowlist(ctx, taskAllowlist)
	}
	if l.Context.Deps.Policy != nil {
		if err := l.Context.Deps.Policy.EvaluateInvoke(ctx, sess, tc.Name, tc.Args); err != nil {
			return ctx, 0, rejectForCallError(err)
		}
	}
	doomPreCount := 0
	if err := l.Nudges.checkDoomLoop(ctx, sessionID, responseID, tc.Name, tc.Args, &doomPreCount); err != nil {
		return ctx, 0, rejectForCallError(err)
	}
	return ctx, doomPreCount, nil
}

// refuseToolCall builds a structured refusal result.
func (l *toolBatch) refuseToolCall(tc api.ToolCall, assistantMessageID string, reject *guidance.Refusal) singleToolOutcome {
	return singleToolOutcome{
		toolName: tc.Name,
		toolMsg:  l.Tools.toolRejectMessage(tc.Name, tc.ID, assistantMessageID, tc.Args, reject),
	}
}

// settlePreInvokeReject closes an opened ledger row.
func (l *toolBatch) settlePreInvokeReject(ctx context.Context, run toolInvocation) (toolInvocation, error) {
	if run.receipt == nil || run.reject == nil {
		return run, nil
	}
	code := run.reject.Code()
	if run.failure == nil {
		run.failure = rejectionFailure(code, "policy_rejection", invocationFailureOwner(run.contract, run.captures), run.reject.Facts.FeedbackFor(code).Details)
	}
	return l.Tools.settleInvocation(ctx, run, api.InvocationStatusRejected, "rejection", code, run.captures.ownerRef)
}

// refusePreInvokeReject records refusal and repeat escalation.
func (l *toolBatch) refusePreInvokeReject(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	tc api.ToolCall,
	assistantMessageID string,
	reject *guidance.Refusal,
) singleToolOutcome {
	code := reject.Code()
	advance := l.Tools.overlayIntegrateRejectEndsToolLoop(ctx, sess, sessionID, code)
	if !advance {
		_ = l.Nudges.recordDoomLoopAttempt(ctx, sessionID, assistantMessageID, tc.Name, tc.Args, code, false)
		// Recorded first so the escalation reads the current total.
		if escalated := l.Nudges.escalateRepeatedCode(ctx, sessionID, tc.Name, reject); escalated != nil {
			combined := *reject
			combined.Body += "\n\n" + escalated.Body
			combined.Facts = combined.Facts.Merge(escalated.Facts)
			reject = &combined
		}
	}
	out := l.refuseToolCall(tc, assistantMessageID, reject)
	out.breakToolLoop = out.breakToolLoop || advance
	return out
}

func (l *toolBatch) settleRejectedInvocation(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	tc api.ToolCall,
	assistantMessageID string,
	run toolInvocation,
) singleToolOutcome {
	if run.failure == nil {
		code := run.facts.PrimaryCode()
		run.failure = rejectionFailure(code, "host_rejection", invocationFailureOwner(run.contract, run.captures), run.facts.FeedbackFor(code).Details)
	}
	settled, err := l.Tools.settleInvocation(ctx, run, api.InvocationStatusRejected, "rejection", run.facts.PrimaryCode(), run.captures.ownerRef)
	if err != nil {
		return l.settlementHostFault(tc, assistantMessageID, run, err)
	}
	return l.settleRejectedToolCall(ctx, sess, sessionID, tc, assistantMessageID, settled)
}

// settleRejectedToolCall records and projects a refusal.
func (l *toolBatch) settleRejectedToolCall(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	tc api.ToolCall,
	assistantMessageID string,
	run toolInvocation,
) singleToolOutcome {
	rejectCode := run.facts.PrimaryCode()
	advance := l.Tools.overlayIntegrateRejectEndsToolLoop(ctx, sess, sessionID, rejectCode)
	// Host-managed failures do not count as caller repetition.
	if !advance && run.failure.CallerFault() {
		_ = l.Nudges.recordDoomLoopAttempt(ctx, sessionID, assistantMessageID, tc.Name, tc.Args, rejectCode, false)
		// Recorded first so the escalation reads the current total.
		if escalated := l.Nudges.escalateRepeatedCode(ctx, sessionID, tc.Name, run.asReject()); escalated != nil {
			run.content += "\n\n" + escalated.Body
			run.facts = run.facts.Merge(escalated.Facts)
		}
	}
	if l.Projection.Deps.OnToolReject != nil {
		l.Projection.Deps.OnToolReject(ctx, sessionID, tc.ID, rejectCode, run.content, run.facts)
	}
	out := singleToolOutcome{
		toolName:      tc.Name,
		toolMsg:       l.Tools.toolRejectMessage(tc.Name, tc.ID, assistantMessageID, tc.Args, run.asReject()),
		breakToolLoop: advance,
	}
	if out.toolMsg.ToolResult != nil {
		out.toolMsg.ToolResult.Invocation = run.receipt
	}
	return out
}

// rejectForCallError projects pre-invocation errors as refusals.
func rejectForCallError(err error) *guidance.Refusal {
	if reject, ok := guidance.RefusalFromError(err); ok {
		return reject
	}
	return guidance.NewRefusal("", err.Error())
}
