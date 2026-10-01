package promptloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

type loopExitKind int

const (
	loopExitNone loopExitKind = iota
	loopExitCompleted
	loopExitToolBoundary
	loopExitGracefulCancel
	loopExitAssertRunnable
	loopExitLLMTimeout
	loopExitSecretWithheld
	loopExitBlockedLoop
)

type promptLoopTurnState struct {
	observePrompt           func(inject.CoordinatorTurnFrame)
	sourceContext           *api.SourceContext
	turnID                  string
	attemptID               string
	lastOutputID            string
	history                 []api.Message
	lastAssistantID         string
	lastAssistantContent    string
	turnTools               []string
	tasksDispatchedCount    int
	turnEndedGuidanceReject bool
	// Read-only streaks count consecutive batches that observed without acting.
	readOnlyBatches        int
	readOnlyStreakTools    []string
	surveyStreakNudged     int
	statusCursorNextOffset *int
	statusCursorPaths      []string
	statusCursorGroupDepth int
	// Blocked streaks track repeated and total rejection codes.
	consecutiveBlockedBatches int
	blockedBatchesThisStreak  int
	blockedStreakCodes        map[string]struct{}
	proseFinishDelivered      bool
	spendSoftStopGranted      bool
	closeoutRetry             closeoutRetryState
	// deferredUserNudges flush when the draft slot closes.
	deferredUserNudges []api.Message
	turnsRanThisRun    int
	// workerProgress tracks durable rounds and the active batch.
	workerProgress *workerprogress.Tracker
	workerJobID    string
	// workerBudget follows the ceiling and budget request between rounds.
	workerBudget workerBudgetWatch
	// draftSlotID identifies the draft updated by the current model turn.
	draftSlotID       string
	draftSlotAppended bool
	// proseFinish is a forced or last-iteration closeout.
	proseFinish bool
	// offeredToolNames distinguishes unstamped requests from an empty tool set.
	offeredToolNames   []string
	offeredToolSchemas map[string]map[string]any
	// secretStorageOverlays retain raw fields until their approval decision.
	secretStorageOverlays map[string]secretStorageOverlay
	// secretWithheldRetries bounds repeated credential approval requests.
	secretWithheldRetries int
	coordinatorFrame      inject.CoordinatorTurnFrame
	coordinatorFrameReady bool
	machine               inject.Machine
	turnToolPlan          toolsurface.Plan
}

func (st *promptLoopTurnState) progress() *workerprogress.Tracker {
	if st == nil {
		return nil
	}
	return st.workerProgress
}

func (st *promptLoopTurnState) workerRunID() string {
	if st == nil {
		return ""
	}
	return st.workerJobID
}

// noteBlockedBatch records a no-op batch and checks both streak limits.
func (st *promptLoopTurnState) noteBlockedBatch(code string) bool {
	if st == nil {
		return false
	}
	st.blockedBatchesThisStreak++
	if st.blockedStreakCodes == nil {
		st.blockedStreakCodes = map[string]struct{}{}
	}
	if _, seen := st.blockedStreakCodes[code]; seen {
		st.consecutiveBlockedBatches++
	} else {
		st.blockedStreakCodes[code] = struct{}{}
	}
	return st.blockedLoopExhausted()
}

func (st *promptLoopTurnState) clearBlockedStreak() {
	if st == nil {
		return
	}
	st.consecutiveBlockedBatches = 0
	st.blockedBatchesThisStreak = 0
	st.blockedStreakCodes = nil
}

func (st *promptLoopTurnState) blockedLoopExhausted() bool {
	if st == nil {
		return false
	}
	return st.consecutiveBlockedBatches >= BlockedLoopRejectCap ||
		st.blockedBatchesThisStreak >= BlockedLoopAbsoluteCap
}

func (st *promptLoopTurnState) proseTurn(sess *api.Session, iterIndex, maxIter int) bool {
	return guard.ProseTurn(sess, iterIndex, maxIter, st != nil && st.proseFinish)
}

// batchMadeProgress reports whether the batch advanced or ended its own cycle.
func batchMadeProgress(history []api.Message, assistantMessageID string) bool {
	assistantMessageID = strings.TrimSpace(assistantMessageID)
	if assistantMessageID == "" {
		return true
	}
	sawToolRow := false
	for _, msg := range history {
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		if strings.TrimSpace(msg.ToolResult.AssistantMessageID) != assistantMessageID {
			continue
		}
		sawToolRow = true
		switch msg.ToolResult.Outcome {
		case api.ToolResultOutcomeRejected, api.ToolResultOutcomeError:
			continue
		default:
			return true
		}
	}
	return !sawToolRow
}

func batchRejectCode(history []api.Message, assistantMessageID string) string {
	assistantMessageID = strings.TrimSpace(assistantMessageID)
	if assistantMessageID == "" {
		return ""
	}
	for _, msg := range history {
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		if strings.TrimSpace(msg.ToolResult.AssistantMessageID) != assistantMessageID {
			continue
		}
		if len(msg.ToolResult.Codes) > 0 {
			return strings.TrimSpace(msg.ToolResult.Codes[0])
		}
	}
	return ""
}

type promptLoopAssistantTurnResult struct {
	continueLoop bool
	breakLoop    bool
	exit         loopExitKind
}

func (l *PromptLoop) processPromptLoopAssistantTurn(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	in PromptRunInput,
	step, maxIter int,
	surfaceID, userPrompt string,
	st *promptLoopTurnState,
	assistantMsg api.Message,
	completion *modelcall.Completion,
) (promptLoopAssistantTurnResult, error) {
	var out promptLoopAssistantTurnResult
	st.lastAssistantID = assistantMsg.ID
	st.lastAssistantContent = assistantMsg.Content
	st.history = append(st.history, assistantMsg)

	if completion != nil && len(completion.ToolCalls) > 0 && st.proseTurn(sess, step, maxIter) {
		offered := workerProseOfferedTools(sess)
		kept := retainOfferedToolCalls(st.history, assistantMsg.ID, completion.ToolCalls, offered)
		if len(kept) != len(completion.ToolCalls) {
			assistantMsg.ToolCalls = kept
			st.history[len(st.history)-1] = assistantMsg
			completion.ToolCalls = kept
			if l.Deps.UpdateMessage != nil {
				_ = l.Deps.UpdateMessage(ctx, sessionID, assistantMsg.ID, assistantMsg)
			}
		}
	}

	if completion == nil || len(completion.ToolCalls) == 0 {
		var unread []jsonshape.Issue
		st.lastAssistantContent, unread = l.maybeCoerceCloseoutContent(ctx, st.history, sessionID, surfaceID, st.lastAssistantContent, st.lastAssistantID)
		invokeAllowed := !st.proseTurn(sess, step, maxIter)
		var blocked bool
		var refusal *guidance.Refusal
		var err error
		st.history, refusal, blocked, err = l.tryRejectNoToolTurn(
			ctx, sess, st.history, userPrompt, st.lastAssistantContent, surfaceID, st.turnTools, sessionID, st.lastAssistantID, st, invokeAllowed,
		)
		if err != nil {
			return out, err
		}
		if blocked {
			if refusal != nil && st.noteBlockedBatch(refusal.Code()) {
				out.breakLoop = true
				out.exit = loopExitBlockedLoop
				return out, nil
			}
			out.continueLoop = true
			return out, nil
		}
		if report, ok := guidance.ParseCoordinatorCompletionReport(st.lastAssistantContent); ok && surface.SurfaceDeliversReport(surfaceID) {
			return l.applyAcceptedCloseoutReport(ctx, sess, sessionID, userPrompt, surfaceID, st, assistantMsg, guidance.CloseoutRead{Report: report, Unread: unread})
		}
	} else {
		if st != nil && st.turnID != "" && st.attemptID != "" && l.Deps.CheckpointTurn != nil {
			checkpoint := fmt.Sprintf(`{"model_output_id":%q,"assistant_message_id":%q}`, st.lastOutputID, assistantMsg.ID)
			if err := l.Deps.CheckpointTurn(ctx, st.turnID, st.attemptID, store.TurnPhaseTools, checkpoint); err != nil {
				return out, err
			}
		}
		var anyTaskEnqueued bool
		var taskEnqueuedThisTurn int
		var lastTaskMessageID string
		var breakToolLoop bool
		var batchTurnTools []string
		var err error
		st.history, batchTurnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID, breakToolLoop, err = l.executeToolCallsInTurn(
			ctx, sess, sessionID, completion.ToolCalls, in.ToolCtx, st.history, userPrompt, st.lastAssistantID, surfaceID, st,
		)
		if err != nil {
			return out, err
		}
		if batchMadeProgress(st.history, assistantMsg.ID) {
			st.clearBlockedStreak()
			st.noteBatchLifecycle(judgeBatch(l.Deps.Tools, st.history, assistantMsg.ID))
		} else {
			code := batchRejectCode(st.history, assistantMsg.ID)
			if !schemaRejectExemptFromBlockedLoop(code) && st.noteBlockedBatch(code) {
				out.breakLoop = true
				out.exit = loopExitBlockedLoop
				return out, nil
			}
		}
		// Close the slot; the next model turn opens a fresh one.
		if err := l.closeCoordinatorDraftSlot(ctx, sessionID, st, assistantMsg.ID); err != nil {
			return out, err
		}
		if sess != nil && sess.IsWorkerChild() && batchHasUnsettledOwner(st.history, assistantMsg.ID) {
			return out, ErrOwnerUnsettled
		}
		st.turnTools = append(st.turnTools, batchTurnTools...)
		st.tasksDispatchedCount += taskEnqueuedThisTurn
		if anyTaskEnqueued {
			if err := l.appendInFlightWorkerRosterNote(ctx, sessionID, sess, st.history, taskEnqueuedThisTurn, lastTaskMessageID); err != nil {
				return out, err
			}
		}
		if breakToolLoop {
			out.breakLoop = true
			out.exit = loopExitToolBoundary
			return out, nil
		}
		if stalled, serr := l.maybeEmitStalledCloseout(ctx, sess, sessionID, userPrompt, surfaceID, st); serr != nil {
			return out, serr
		} else if stalled {
			out.breakLoop = true
			out.exit = loopExitCompleted
			return out, nil
		}
		st.history, err = l.reloadHistoryAfterToolCompaction(ctx, sessionID, sess, surfaceID, st.history, st)
		if err != nil {
			return out, err
		}
		out.continueLoop = true
		return out, nil
	}

	committed, err := l.commitGuardedAssistantTurn(ctx, sess, sessionID, st.history, userPrompt, surfaceID, assistantMsg)
	if err != nil {
		return out, err
	}
	st.history[len(st.history)-1] = committed
	if err := l.closeCoordinatorDraftSlot(ctx, sessionID, st, committed.ID); err != nil {
		return out, err
	}
	if st.proseTurn(sess, step, maxIter) {
		st.proseFinishDelivered = true
	}
	out.breakLoop = true
	out.exit = loopExitCompleted
	return out, nil
}

func (l *PromptLoop) applyAcceptedCloseoutReport(
	ctx context.Context,
	sess *api.Session,
	sessionID, userPrompt, surfaceID string,
	st *promptLoopTurnState,
	assistantMsg api.Message,
	read guidance.CloseoutRead,
) (promptLoopAssistantTurnResult, error) {
	var out promptLoopAssistantTurnResult
	commitOut, cerr := l.handleAcceptedCloseoutReport(
		ctx, sess, sessionID, userPrompt, surfaceID, st, st.history, assistantMsg, read,
	)
	if cerr != nil {
		return out, cerr
	}
	st.history = commitOut.history
	if commitOut.retry {
		st.turnEndedGuidanceReject = false
		out.continueLoop = true
		return out, nil
	}
	if commitOut.endWithoutAssemble {
		st.turnEndedGuidanceReject = true
		out.continueLoop = false
		out.breakLoop = true
		out.exit = loopExitCompleted
		return out, nil
	}
	if commitOut.exhausted {
		// Exhaustion preserves the pinned report and latest proposed citations.
		emitOut, eerr := l.emitAssembledCloseout(
			ctx, sess, sessionID, userPrompt, surfaceID, commitOut.draftedContent, st.closeoutRetry.codes, st, st.history,
		)
		if eerr != nil {
			return out, eerr
		}
		if emitOut.committed {
			st.history = emitOut.history
			st.lastAssistantContent = emitOut.assistantMsg.Content
			st.proseFinishDelivered = true
			out.breakLoop = true
			out.exit = loopExitCompleted
			return out, nil
		}
		st.turnEndedGuidanceReject = true
		if err := l.closeCoordinatorDraftSlot(ctx, sessionID, st, st.draftSlotID); err != nil {
			return out, err
		}
		out.breakLoop = true
		out.exit = loopExitCompleted
		return out, nil
	}
	if !commitOut.committed {
		st.turnEndedGuidanceReject = true
		if err := l.closeCoordinatorDraftSlot(ctx, sessionID, st, st.draftSlotID); err != nil {
			return out, err
		}
		out.breakLoop = true
		out.exit = loopExitCompleted
		return out, nil
	}
	st.lastAssistantContent = commitOut.assistantMsg.Content
	for i := range st.history {
		if st.history[i].ID == commitOut.assistantMsg.ID {
			st.history[i] = commitOut.assistantMsg
			break
		}
	}
	st.proseFinishDelivered = true
	out.breakLoop = true
	out.exit = loopExitCompleted
	return out, nil
}

func (l *PromptLoop) maybeEmitStalledCloseout(
	ctx context.Context,
	sess *api.Session,
	sessionID, userPrompt, surfaceID string,
	st *promptLoopTurnState,
) (committed bool, err error) {
	if !l.closeoutFuseTripped(ctx, sess, sessionID, surfaceID) {
		return false, nil
	}
	emitOut, eerr := l.emitStalledCloseout(ctx, sess, sessionID, userPrompt, surfaceID, st, st.history)
	if eerr != nil {
		return false, eerr
	}
	if !emitOut.committed {
		return false, nil
	}
	st.history = emitOut.history
	st.lastAssistantContent = emitOut.assistantMsg.Content
	st.proseFinishDelivered = true
	return true, nil
}

func workersIdleForSynthesis(l *PromptLoop, ctx context.Context, sess *api.Session) bool {
	if l == nil || l.Deps.ImplementSessionState == nil {
		return true
	}
	return l.Deps.ImplementSessionState(ctx, sess).WorkersInFlight == 0
}

func (l *PromptLoop) maybeNotifyGroundedSynthesisAccepted(
	ctx context.Context,
	sess *api.Session,
	sessionID, surfaceID string,
	history []api.Message,
	workersIdle bool,
) {
	if l == nil || l.Deps.OnGroundedSynthesisAccepted == nil || !workersIdle {
		return
	}
	if strings.TrimSpace(surfaceID) != "implement_synthesis" {
		return
	}
	if l.Deps.ImplementSessionState != nil {
		state := l.Deps.ImplementSessionState(ctx, sess)
		if len(state.PendingOverlayIDs) > 0 {
			return
		}
	}
	l.Deps.OnGroundedSynthesisAccepted(ctx, sess, sessionID)
}

func (l *PromptLoop) finalizePromptLoopRun(
	ctx context.Context,
	sess *api.Session,
	sessionID, profileID, userPrompt string,
	maxIter int,
	closeoutNudgeSent bool,
	loopExit loopExitKind,
	in PromptRunInput,
	st *promptLoopTurnState,
) (*PromptRunResult, error) {
	if sess.IsWorkerChild() {
		l.publishWorkerProgress(ctx, sess, st.workerJobID, st.progress().Current(), true)
	}
	blockedLiveCommandParked := false
	if loopExit == loopExitBlockedLoop && !sess.IsWorkerChild() && l.Deps.ParkBlockedLiveCommands != nil {
		blockedLiveCommandParked = l.Deps.ParkBlockedLiveCommands(ctx, sessionID)
		if blockedLiveCommandParked {
			// The process terminal envelope supplies the current state.
			st.lastAssistantID = ""
			st.lastAssistantContent = ""
			st.turnEndedGuidanceReject = true
		}
	}
	// A refused credential ends without another provider request.
	if !blockedLiveCommandParked && !st.proseFinishDelivered && strings.TrimSpace(st.lastAssistantID) != "" &&
		loopExit != loopExitCompleted && loopExit != loopExitToolBoundary &&
		loopExit != loopExitGracefulCancel && loopExit != loopExitSecretWithheld {
		reason := TurnCloseoutIterationCap
		switch loopExit {
		case loopExitAssertRunnable:
			reason = TurnCloseoutStopped
		case loopExitLLMTimeout:
			reason = TurnCloseoutLLMTimeout
		case loopExitBlockedLoop:
			reason = TurnCloseoutBlockedLoop
		case loopExitNone, loopExitCompleted, loopExitToolBoundary, loopExitGracefulCancel, loopExitSecretWithheld:
		}
		closedHistory, aid, content, cerr := l.runEarlyTurnCloseout(ctx, sess, sessionID, profileID, userPrompt, st.history, maxIter, reason, "", closeoutNudgeSent, in, st)
		if cerr != nil {
			return nil, cerr
		}
		st.history = closedHistory
		if aid != "" {
			st.lastAssistantID = aid
			st.lastAssistantContent = content
		}
	}

	// A resumed worker with no remaining budget receives an exhaustion summary.
	if st.lastAssistantID == "" && st.turnsRanThisRun == 0 && sess.IsWorkerChild() && !st.turnEndedGuidanceReject {
		closedHistory, aid, content, cerr := l.runEarlyTurnCloseout(ctx, sess, sessionID, profileID, userPrompt, st.history, maxIter, TurnCloseoutIterationCap, "", closeoutNudgeSent, in, st)
		if cerr != nil {
			return nil, cerr
		}
		st.history = closedHistory
		if aid != "" {
			st.lastAssistantID = aid
			st.lastAssistantContent = content
		}
	}

	if err := l.maybeWithdrawCoordinatorDraft(ctx, sess, sessionID, st); err != nil {
		return nil, err
	}
	if err := l.flushDeferredUserNudges(ctx, sessionID, st); err != nil {
		return nil, err
	}
	if st.lastAssistantID == "" {
		if !st.turnEndedGuidanceReject && loopExit != loopExitSecretWithheld {
			return nil, fmt.Errorf("prompt produced no assistant message")
		}
		return &PromptRunResult{
			TurnTools:            st.turnTools,
			TasksDispatchedCount: st.tasksDispatchedCount,
		}, nil
	}
	return &PromptRunResult{
		LastAssistantID:      st.lastAssistantID,
		LastOutputID:         st.lastOutputID,
		LastAssistantContent: st.lastAssistantContent,
		TurnTools:            st.turnTools,
		TasksDispatchedCount: st.tasksDispatchedCount,
	}, nil
}
