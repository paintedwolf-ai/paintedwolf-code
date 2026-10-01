package promptloop

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

const concurrentToolCallLimit = spawn.MaxConcurrentToolCalls

func registryContract(reg tools.ToolRegistry, name string) (toolcontract.Contract, bool) {
	if reg == nil {
		return toolcontract.Contract{}, false
	}
	def, ok := reg.Definition(name)
	return def.Contract, ok
}

func reorderToolBatchExecution(reg tools.ToolRegistry, calls []api.ToolCall) []api.ToolCall {
	if len(calls) < 2 {
		return calls
	}
	var normal, late, terminal []api.ToolCall
	for i := range calls {
		contract, _ := registryContract(reg, calls[i].Name)
		switch contract.Order {
		case toolcontract.TurnOrderLate:
			late = append(late, calls[i])
		case toolcontract.TurnOrderTerminal:
			terminal = append(terminal, calls[i])
		default:
			normal = append(normal, calls[i])
		}
	}
	out := append([]api.ToolCall(nil), normal...)
	out = append(out, late...)
	out = append(out, terminal...)
	return out
}

func concurrentRunLimit(reg tools.ToolRegistry, run []api.ToolCall) int {
	limit := concurrentToolCallLimit
	for i := range run {
		contract, ok := registryContract(reg, run[i].Name)
		if ok && contract.BatchConcurrencyLimit > 0 && contract.BatchConcurrencyLimit < limit {
			limit = contract.BatchConcurrencyLimit
		}
	}
	return limit
}

type toolCallOutcome struct {
	index              int
	toolName           string
	toolArgs           map[string]any
	toolMsg            api.Message
	taskCommitted      bool
	handleEligible     bool
	taskMessageID      string
	agentNote          *tools.AgentNoteCapture
	committed          bool
	storedID           string
	persistedStored    []api.Message
	persistedTransient []*api.Message
	// err is a failure that produced no durable result.
	err error
	// endTurn is a host fault that ends the turn once toolMsg is durable.
	endTurn error
}

func partitionToolBatchRuns(reg tools.ToolRegistry, calls []api.ToolCall) [][]api.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	var runs [][]api.ToolCall
	i := 0
	for i < len(calls) {
		groupable := false
		if i+1 < len(calls) {
			left, leftOK := registryContract(reg, calls[i].Name)
			right, rightOK := registryContract(reg, calls[i+1].Name)
			groupable = leftOK && rightOK && toolcontract.BatchGroupableCalls(
				calls[i].Name, calls[i].Args, left, calls[i+1].Name, calls[i+1].Args, right,
			)
		}
		if groupable {
			j := i + 1
			for j < len(calls) {
				previous, previousOK := registryContract(reg, calls[j-1].Name)
				current, currentOK := registryContract(reg, calls[j].Name)
				if !previousOK || !currentOK || !toolcontract.BatchGroupableCalls(
					calls[j-1].Name, calls[j-1].Args, previous, calls[j].Name, calls[j].Args, current,
				) {
					break
				}
				j++
			}
			runs = append(runs, calls[i:j])
			i = j
			continue
		}
		runs = append(runs, calls[i:i+1])
		i++
	}
	return runs
}

// toolContextForCall refreshes the session tool context before execution.
func (l *PromptLoop) toolContextForCall(ctx context.Context, sess *api.Session, base tools.ToolContext, machine inject.Machine) (tools.ToolContext, error) {
	if l == nil || l.Deps.RefreshToolContext == nil || sess == nil {
		return base, nil
	}
	refreshed, err := l.Deps.RefreshToolContext(ctx, sess, machine)
	if err != nil {
		return tools.ToolContext{}, err
	}
	refreshed.TurnSurfaceID = base.TurnSurfaceID
	refreshed.TurnToolPlan = base.TurnToolPlan
	refreshed.TurnOfferedToolNames = slices.Clone(base.TurnOfferedToolNames)
	refreshed.TurnOfferedToolSchemas = base.TurnOfferedToolSchemas
	refreshed.ModelSourceContext = base.ModelSourceContext
	refreshed.EditorReadBases = base.EditorReadBases
	refreshed.TurnWritePinRootID = base.TurnWritePinRootID
	refreshed.TurnWritePinGlobs = append([]string(nil), base.TurnWritePinGlobs...)
	return refreshed, nil
}

func (l *PromptLoop) executeToolCallsInTurn(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	toolCalls []api.ToolCall,
	baseToolCtx tools.ToolContext,
	history []api.Message,
	userPrompt string,
	assistantMessageID string,
	turnSurfaceID string,
	st *promptLoopTurnState,
) ([]api.Message, []string, bool, int, string, bool, error) {
	startHolds := l.batchStartHolds(ctx, sessionID, turnSurfaceID)
	if st != nil && st.spendSoftStopGranted {
		toolCalls = filterSpendSoftStopToolCalls(toolCalls)
	}
	toolCalls = reorderToolBatchExecution(l.Deps.Tools, toolCalls)
	toolCalls = sanitizeToolCallsForExecution(toolCalls)
	// Persist only storage-safe tool results.
	history = patchAssistantToolCalls(history, assistantMessageID, toolCalls)
	l.syncAssistantToolCallsOnStore(ctx, sessionID, assistantMessageID, history, toolCalls)
	hasAssistant := strings.TrimSpace(assistantMessageID) != ""
	if l.Deps.AssertRunnable != nil {
		if err := l.Deps.AssertRunnable(ctx, sessionID); err != nil {
			if hasAssistant {
				return history, nil, false, 0, "", true, nil
			}
			return history, nil, false, 0, "", false, err
		}
	}
	if baseToolCtx.EditorDocuments != nil {
		baseToolCtx.EditorReadBases = tools.NewAgentReadBases(baseToolCtx.EditorDocuments.FreezeAgentReads(baseToolCtx.ProjectID, baseToolCtx.SessionID))
	}
	baseToolCtx.TurnSurfaceID = strings.TrimSpace(turnSurfaceID)
	frame := inject.CoordinatorTurnFrame{}
	machine := inject.Machine{}
	if st != nil {
		frame = st.coordinatorFrame
		machine = st.machine
	}
	if st != nil {
		baseToolCtx.TurnToolPlan = st.turnToolPlan
		baseToolCtx.TurnOfferedToolNames = slices.Clone(st.offeredToolNames)
		baseToolCtx.TurnOfferedToolSchemas = st.offeredToolSchemas
		baseToolCtx.ModelSourceContext = st.sourceContext
	}
	taskAllowlist := taskSpawnAllowlistForTurn(frame)
	var turnTools []string
	var anyTaskEnqueued bool
	var taskEnqueuedThisTurn int
	var lastTaskMessageID string
	var lastToolTS time.Time
	assistantCommitted := false
	commitAssistant := func() error {
		if !hasAssistant || assistantCommitted {
			return nil
		}
		var err error
		history, err = l.commitProvisionalAssistantInHistory(ctx, sess, sessionID, history, userPrompt, turnSurfaceID, assistantMessageID)
		if err != nil {
			return err
		}
		// Keep the draft open through tool settlement.
		assistantCommitted = true
		return nil
	}
	// Commit tool cards before execution.
	if err := commitAssistant(); err != nil {
		return history, nil, false, 0, "", false, err
	}
	// Batch closure survives request cancellation.
	l.publishWorkerProgress(ctx, sess, st.workerRunID(), st.progress().BeginBatch(len(toolCalls)), false)
	defer func() {
		l.publishWorkerProgress(context.WithoutCancel(ctx), sess, st.workerRunID(), st.progress().EndBatch(), false)
	}()

	for _, run := range partitionToolBatchRuns(l.Deps.Tools, toolCalls) {
		if len(run) == 1 {
			tctx, refreshErr := l.toolContextForCall(ctx, sess, baseToolCtx, machine)
			if refreshErr != nil {
				return history, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID, false, refreshErr
			}
			out := l.executeOneToolCall(ctx, sess, sessionID, userPrompt, history, run[0], tctx, taskAllowlist, assistantMessageID, frame.RunContext, st != nil && st.proseFinish)
			var err error
			history, err = l.persistClassifiedToolOutcome(
				ctx, sessionID, sess, history, toolCallOutcome{
					toolName:       out.toolName,
					toolArgs:       run[0].Args,
					toolMsg:        out.toolMsg,
					handleEligible: out.handleEligible,
					agentNote:      out.agentNote,
				}, &lastToolTS, st,
			)
			if err != nil {
				return history, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID, false, err
			}
			turnTools = l.settleToolRow(ctx, sess, sessionID, out.toolName, turnTools, st)
			if out.taskCommitted {
				anyTaskEnqueued = true
				taskEnqueuedThisTurn++
				lastTaskMessageID = out.toolMsg.ID
			}
			if out.endTurn != nil {
				history, turnTools, err = l.closeBatchForHostFault(ctx, sess, sessionID, assistantMessageID, history, toolCalls, turnTools, &lastToolTS, st, out.endTurn)
				return history, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID, false, err
			}
			if out.breakToolLoop || loopwake.WaitCompletionEndsCycle(out.toolName, out.completion) ||
				loopwake.AskUserEndsCycle(out.toolName, out.toolMsg.Content, true) ||
				native.WorkerToolEndsCycle(out.toolName, out.toolMsg.ToolResult) ||
				l.hostHITLParked(ctx, sessionID, startHolds) {
				history, turnTools, err = l.settleUnattemptedCalls(ctx, sess, sessionID, assistantMessageID, history, toolCalls, turnTools, &lastToolTS, st)
				return history, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID, true, err
			}
			continue
		}

		commit := newParallelBatchCommit(
			history, run, &lastToolTS, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID, st, sess, sessionID,
		)
		outcomes := l.runConcurrentToolBatch(ctx, sess, sessionID, userPrompt, history, run, baseToolCtx, taskAllowlist, assistantMessageID, frame.RunContext, machine, commit, st != nil && st.proseFinish)

		var err error
		history, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID, err =
			l.applyParallelToolOutcomes(ctx, outcomes, commit)
		if fault := batchHostFault(outcomes); err == nil && fault != nil {
			history, turnTools, err = l.closeBatchForHostFault(ctx, sess, sessionID, assistantMessageID, history, toolCalls, turnTools, &lastToolTS, st, fault)
		}
		if err != nil {
			return history, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID, false, err
		}
		if l.hostHITLParked(ctx, sessionID, startHolds) {
			history, turnTools, err = l.settleUnattemptedCalls(ctx, sess, sessionID, assistantMessageID, history, toolCalls, turnTools, &lastToolTS, st)
			return history, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID, true, err
		}
	}
	return history, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID, false, nil
}

// batchHolds are the host-managed waits a batch may run under.
type batchHolds struct {
	// approval was already awaiting when the batch started.
	approval bool
	// host held the phase at batch start and the turn answers the person.
	answeringUnderHost bool
}

func (l *PromptLoop) batchStartHolds(ctx context.Context, sessionID, turnSurfaceID string) batchHolds {
	if l == nil {
		return batchHolds{}
	}
	return batchHolds{
		approval: l.Deps.HumanApprovalAwaiting != nil && l.Deps.HumanApprovalAwaiting(ctx, sessionID),
		answeringUnderHost: strings.TrimSpace(turnSurfaceID) == tools.SurfaceAwaitHost &&
			l.Deps.HostObligationHeld != nil && l.Deps.HostObligationHeld(ctx, sessionID),
	}
}

// hostHITLParked reports a host-managed wait that ends the batch. A turn
// already running under one continues to its reply: review edits under an
// awaiting approval, and the person's turns while the host holds the phase.
func (l *PromptLoop) hostHITLParked(ctx context.Context, sessionID string, holds batchHolds) bool {
	if l == nil {
		return false
	}
	if !holds.approval && l.Deps.HumanApprovalAwaiting != nil && l.Deps.HumanApprovalAwaiting(ctx, sessionID) {
		return true
	}
	return !holds.answeringUnderHost && l.Deps.HostObligationHeld != nil && l.Deps.HostObligationHeld(ctx, sessionID)
}

// runConcurrentToolBatch executes bounded concurrent calls and preserves call order.
func (l *PromptLoop) runConcurrentToolBatch(
	ctx context.Context,
	sess *api.Session,
	sessionID, userPrompt string,
	history []api.Message,
	run []api.ToolCall,
	baseToolCtx tools.ToolContext,
	taskAllowlist []string,
	assistantMessageID string,
	runCtx api.CoordinatorRunContext,
	machine inject.Machine,
	commit *parallelBatchCommit,
	proseTurn bool,
) []toolCallOutcome {
	outcomes := make([]toolCallOutcome, len(run))
	sem := make(chan struct{}, concurrentRunLimit(l.Deps.Tools, run))
	var wg sync.WaitGroup
	for idx, tc := range run {
		wg.Add(1)
		go func(i int, call api.ToolCall) {
			defer wg.Done()
			// Confine panics to their call outcome.
			defer func() {
				r := recover()
				if r == nil {
					return
				}
				observability.LogRecoveredPanic("promptloop.toolcall", r,
					"tool", call.Name, "tool_call_id", call.ID)
				outcomes[i] = toolCallOutcome{
					index: i,
					err:   fmt.Errorf("tool %s panicked: %v", call.Name, r),
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			tctx, refreshErr := l.toolContextForCall(ctx, sess, baseToolCtx, machine)
			if refreshErr != nil {
				outcomes[i] = toolCallOutcome{index: i, err: refreshErr}
				return
			}
			tctx.Out = &tools.ToolInvocationOut{}
			out := l.executeOneToolCall(ctx, sess, sessionID, userPrompt, history, call, tctx, taskAllowlist, assistantMessageID, runCtx, proseTurn)
			outcomes[i] = toolCallOutcome{
				index:          i,
				toolName:       out.toolName,
				toolArgs:       call.Args,
				toolMsg:        out.toolMsg,
				taskCommitted:  out.taskCommitted,
				handleEligible: out.handleEligible,
				taskMessageID:  out.toolMsg.ID,
				agentNote:      out.agentNote,
				endTurn:        out.endTurn,
			}
			l.appendParallelResult(ctx, commit, &outcomes[i])
		}(idx, tc)
	}
	wg.Wait()
	return outcomes
}

// applyParallelToolOutcomes retains every settled result before returning a host error.
func (l *PromptLoop) applyParallelToolOutcomes(
	ctx context.Context,
	outcomes []toolCallOutcome,
	commit *parallelBatchCommit,
) ([]api.Message, []string, bool, int, string, error) {
	if err := l.finishParallelToolOutcomes(ctx, commit, outcomes); err != nil {
		history, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID := commitSnapshot(commit)
		return history, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID, err
	}
	history, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID := commitSnapshot(commit)
	for i := 0; i < len(outcomes); i++ {
		out := outcomes[i]
		if out.err != nil {
			return history, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID, out.err
		}
	}
	return history, turnTools, anyTaskEnqueued, taskEnqueuedThisTurn, lastTaskMessageID, nil
}

func commitSnapshot(commit *parallelBatchCommit) ([]api.Message, []string, bool, int, string) {
	if commit == nil {
		return nil, nil, false, 0, ""
	}
	return commit.history, commit.turnTools, commit.anyTaskEnqueued, commit.taskEnqueuedThisTurn, commit.lastTaskMessageID
}

func (l *PromptLoop) syncAssistantToolCallsOnStore(
	ctx context.Context,
	sessionID, assistantMessageID string,
	history []api.Message,
	toolCalls []api.ToolCall,
) {
	if l == nil || l.Deps.UpdateMessage == nil {
		return
	}
	assistantMessageID = strings.TrimSpace(assistantMessageID)
	if assistantMessageID == "" {
		return
	}
	for _, msg := range history {
		if msg.ID != assistantMessageID {
			continue
		}
		patch := msg
		patch.ToolCalls = toolCalls
		_ = l.Deps.UpdateMessage(ctx, sessionID, assistantMessageID, patch)
		return
	}
}

// settleToolRow advances cards after the result is durable.
func (l *PromptLoop) settleToolRow(
	ctx context.Context,
	sess *api.Session,
	sessionID, toolName string,
	turnTools []string,
	st *promptLoopTurnState,
) []string {
	l.announceToolAskAfterCommit(ctx, sessionID, toolName)
	l.publishWorkerProgress(ctx, sess, st.workerRunID(), st.progress().SettleCall(), false)
	return append(turnTools, toolName)
}

// announceToolAskAfterCommit adds the question card after its tool row is durable.
func (l *PromptLoop) announceToolAskAfterCommit(ctx context.Context, sessionID, toolName string) {
	if l == nil || l.Deps.AnnouncePendingToolAsk == nil {
		return
	}
	if strings.TrimSpace(strings.ToLower(toolName)) != "ask_user" {
		return
	}
	l.Deps.AnnouncePendingToolAsk(ctx, sessionID)
}

type singleToolOutcome struct {
	toolName       string
	toolMsg        api.Message
	completion     *api.ToolCompletion
	taskCommitted  bool
	handleEligible bool
	breakToolLoop  bool
	agentNote      *tools.AgentNoteCapture
	// endTurn is a host fault that ends the turn once toolMsg is durable.
	endTurn error
}

// attachRejectReceipt binds the settled invocation to its own tool result.
func attachRejectReceipt(out singleToolOutcome, receipt *api.InvocationReceipt) singleToolOutcome {
	if out.toolMsg.ToolResult != nil {
		out.toolMsg.ToolResult.Invocation = receipt
	}
	return out
}

func (l *PromptLoop) executeOneToolCall(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	userPrompt string,
	history []api.Message,
	tc api.ToolCall,
	toolCtx tools.ToolContext,
	taskAllowlist []string,
	assistantMessageID string,
	runCtx api.CoordinatorRunContext,
	proseTurn bool,
) singleToolOutcome {
	ctx = tools.WithRecoveryTools(ctx, toolCtx.TurnOfferedToolNames)
	if proseTurn && !workerProseAllowsTool(sess, tc.Name) {
		return l.refuseToolCall(tc, assistantMessageID, l.rejectToolOccurrence(ctx, sess, tc, toolCtx, "TOOL_INVOKE_PROSE_TURN", nil))
	}
	ctx, doomPreCount, reject := l.preflightToolCall(ctx, sess, sessionID, assistantMessageID, tc, taskAllowlist)
	if reject != nil {
		return l.refuseToolCall(tc, assistantMessageID, reject)
	}

	run := l.executeToolCall(ctx, sess, sessionID, userPrompt, history, tc, toolCtx, taskAllowlist, doomPreCount, assistantMessageID, runCtx)
	if !run.invoked && run.reject != nil {
		settled, err := l.settlePreInvokeReject(ctx, run)
		if err != nil {
			return l.settlementHostFault(tc, assistantMessageID, run, err)
		}
		out := l.refusePreInvokeReject(ctx, sess, sessionID, tc, assistantMessageID, settled.reject)
		return attachRejectReceipt(out, settled.receipt)
	}
	maxBytes, maxSpillBytes := l.toolResultLimits(ctx, sess)
	if run.succeeded() {
		if code, data := tooloutput.ClassifyEmitReject(tc.Name, tc.Args, run.content, maxSpillBytes); code != "" {
			spillWireLog.Info("tool emit rejected",
				"code", code,
				"tool", tc.Name,
				"bytes", data["bytes"],
				"cap", data["cap"])
			run = l.refuseOutputDelivery(ctx, sess, tc, toolCtx, run, code, data)
		}
	}
	if !run.succeeded() {
		run.captures.note = nil
	}
	if run.facts.Resolution() == api.ToolResultOutcomeRejected {
		return l.settleRejectedInvocation(ctx, sess, sessionID, tc, assistantMessageID, run)
	}

	// Compact only after stamping evidence handles.
	storageProjection := l.projectToolResultForStorage(ctx, run.content, tc.Args)
	projected := l.truncateToolResultForSession(
		ctx,
		tc.Name,
		storageProjection,
		run.content,
		maxBytes,
		maxSpillBytes,
		sess,
	)
	if projected.reject != nil {
		run = l.refuseOutputDelivery(ctx, sess, tc, toolCtx, run, projected.reject.Code, projected.reject.Data)
		return l.settleRejectedInvocation(ctx, sess, sessionID, tc, assistantMessageID, run)
	}
	run.content = projected.content
	run.facts = run.facts.Merge(projected.facts)
	run.captures.sourceReads = agentpresence.WithinLimit(run.captures.sourceReads, projected.limit)
	// Spill refusal ends the call.
	if run.facts.Resolution() == api.ToolResultOutcomeRejected {
		return l.settleRejectedInvocation(ctx, sess, sessionID, tc, assistantMessageID, run)
	}

	// Policy and transcript use the same result provenance.
	toolOrigin := api.MessageOriginTool
	if ingestion.IsRetrievalTool(tc.Name) {
		toolOrigin = api.MessageOriginRetrieval
	}
	if reject, blocked, transformed, changed := l.evaluateContentAnchor(ctx, sess, oar.AnchorContentToolResult, []oar.ContentSegment{
		oarContentSegment(run.content, api.MessageRoleTool, toolOrigin, api.ContentAuthorityNone, api.ContentTrustTierUntrusted, tc.Name),
	}, tc.Name, tc.Args); blocked {
		run.failure = rejectionFailure(reject.Code(), "content_policy_rejection", invocationFailureOwner(run.contract, run.captures), reject.Facts.FeedbackFor(reject.Code()).Details)
		settled, settleErr := l.settleInvocation(ctx, run, api.InvocationStatusRejected, "content_policy", reject.Code(), run.captures.ownerRef)
		if settleErr != nil {
			return l.settlementHostFault(tc, assistantMessageID, run, settleErr)
		}
		return attachRejectReceipt(l.refuseToolCall(tc, assistantMessageID, reject), settled.receipt)
	} else if changed {
		run.replaceContent(transformed)
	}

	toolMsg := l.composeToolResultMessage(ctx, sess, sessionID, tc, assistantMessageID, toolOrigin, &run)
	settled, settleErr := l.settleToolResult(ctx, tc, toolCtx, toolMsg, run)
	if settleErr != nil {
		return l.settlementHostFault(tc, assistantMessageID, run, settleErr)
	}
	run = settled
	l.recordReturnedText(ctx, sessionID, tc, run)
	l.recordSourceRunEvidence(ctx, sessionID, sess, tc.Name, run)
	if run.succeeded() && l.Deps.ToolObserved != nil {
		l.Deps.ToolObserved(ctx, sess, toolCtx, tc.Name)
	}
	if toolMsg.ToolResult != nil {
		toolMsg.ToolResult.Invocation = run.receipt
	}
	return singleToolOutcome{
		toolName:       tc.Name,
		toolMsg:        toolMsg,
		completion:     run.captures.completion,
		taskCommitted:  taskSpawnCommitted(tc.Name, toolMsg.ToolResult, run.succeeded()),
		handleEligible: sess != nil && toolEvidenceEligible(run, tc.Name, tc.Args),
		agentNote:      run.captures.note,
	}
}

func (l *PromptLoop) settleToolResult(
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
		run.sourceRevision, run.sourceRootDigest = sourceledger.VerificationState(ctx, toolCtx.SourceLedger, tools.HostWriteRoot(toolCtx))
	}
	if source := run.captures.sourceRun; source != nil {
		run.sourceRevision, run.sourceRootDigest = source.SourceRevision, source.SourceRootDigest
	}
	return l.settleInvocation(ctx, run, status, evidenceKind, toolMsg.ID, ownerRef)
}

func (l *PromptLoop) toolResultLimits(ctx context.Context, sess *api.Session) (int, int) {
	if l.Deps.Limits == nil {
		return 0, 0
	}
	limits := l.Deps.Limits(ctx, sess)
	return limits.MaxToolResultBytes, limits.MaxToolSpillBytes
}

func (l *PromptLoop) preflightToolCall(
	ctx context.Context,
	sess *api.Session,
	sessionID, responseID string,
	tc api.ToolCall,
	taskAllowlist []string,
) (context.Context, int, *guidance.Refusal) {
	if taskAllowlist != nil {
		ctx = toolpolicy.WithTaskSpawnAllowlist(ctx, taskAllowlist)
	}
	if l.Deps.Policy != nil {
		if err := l.Deps.Policy.EvaluateInvoke(ctx, sess, tc.Name, tc.Args); err != nil {
			return ctx, 0, rejectForCallError(err)
		}
	}
	doomPreCount := 0
	if err := l.checkDoomLoop(ctx, sessionID, responseID, tc.Name, tc.Args, &doomPreCount); err != nil {
		return ctx, 0, rejectForCallError(err)
	}
	return ctx, doomPreCount, nil
}

// refuseToolCall builds a structured refusal result.
func (l *PromptLoop) refuseToolCall(tc api.ToolCall, assistantMessageID string, reject *guidance.Refusal) singleToolOutcome {
	return singleToolOutcome{
		toolName: tc.Name,
		toolMsg:  l.toolRejectMessage(tc.Name, tc.ID, assistantMessageID, tc.Args, reject),
	}
}

// settlePreInvokeReject closes an opened ledger row.
func (l *PromptLoop) settlePreInvokeReject(ctx context.Context, run toolInvocation) (toolInvocation, error) {
	if run.receipt == nil || run.reject == nil {
		return run, nil
	}
	code := run.reject.Code()
	if run.failure == nil {
		run.failure = rejectionFailure(code, "policy_rejection", invocationFailureOwner(run.contract, run.captures), run.reject.Facts.FeedbackFor(code).Details)
	}
	return l.settleInvocation(ctx, run, api.InvocationStatusRejected, "rejection", code, run.captures.ownerRef)
}

// refusePreInvokeReject records refusal and repeat escalation.
func (l *PromptLoop) refusePreInvokeReject(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	tc api.ToolCall,
	assistantMessageID string,
	reject *guidance.Refusal,
) singleToolOutcome {
	code := reject.Code()
	advance := l.overlayIntegrateRejectEndsToolLoop(ctx, sess, sessionID, code)
	if !advance {
		_ = l.recordDoomLoopAttempt(ctx, sessionID, assistantMessageID, tc.Name, tc.Args, code, false)
		// Recorded first so the escalation reads the current total.
		if escalated := l.escalateRepeatedCode(ctx, sessionID, tc.Name, reject); escalated != nil {
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

func (l *PromptLoop) settleRejectedInvocation(
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
	settled, err := l.settleInvocation(ctx, run, api.InvocationStatusRejected, "rejection", run.facts.PrimaryCode(), run.captures.ownerRef)
	if err != nil {
		return l.settlementHostFault(tc, assistantMessageID, run, err)
	}
	return l.settleRejectedToolCall(ctx, sess, sessionID, tc, assistantMessageID, settled)
}

// settleRejectedToolCall records and projects a refusal.
func (l *PromptLoop) settleRejectedToolCall(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	tc api.ToolCall,
	assistantMessageID string,
	run toolInvocation,
) singleToolOutcome {
	rejectCode := run.facts.PrimaryCode()
	advance := l.overlayIntegrateRejectEndsToolLoop(ctx, sess, sessionID, rejectCode)
	// Host-managed failures do not count as caller repetition.
	if !advance && run.failure.CallerFault() {
		_ = l.recordDoomLoopAttempt(ctx, sessionID, assistantMessageID, tc.Name, tc.Args, rejectCode, false)
		// Recorded first so the escalation reads the current total.
		if escalated := l.escalateRepeatedCode(ctx, sessionID, tc.Name, run.asReject()); escalated != nil {
			run.content += "\n\n" + escalated.Body
			run.facts = run.facts.Merge(escalated.Facts)
		}
	}
	if l.Deps.OnToolReject != nil {
		l.Deps.OnToolReject(ctx, sessionID, tc.ID, rejectCode, run.content, run.facts)
	}
	out := singleToolOutcome{
		toolName:      tc.Name,
		toolMsg:       l.toolRejectMessage(tc.Name, tc.ID, assistantMessageID, tc.Args, run.asReject()),
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

// stampCommitOrderTS assigns monotonically increasing timestamps in call order.
func stampCommitOrderTS(msg *api.Message, last *time.Time) {
	now := time.Now().UTC()
	if !now.After(*last) {
		now = last.Add(time.Microsecond)
	}
	*last = now
	msg.CreatedAt = now
}

// tagToolHandleOnCommit persists the evidence record and stamps the host handle on the tool result.
func (l *PromptLoop) tagToolHandleOnCommit(ctx context.Context, sessionID string, sess *api.Session, toolName string, args map[string]any, msg *api.Message, eligible bool) error {
	if msg == nil {
		return nil
	}
	if sess != nil && toolName == "verify" && l.Deps.ConfirmVerifyResult != nil &&
		msg.ToolResult != nil && msg.ToolResult.Outcome == api.ToolResultOutcomeCompleted {
		content := strings.TrimSpace(msg.ToolResult.Content)
		if content == "" {
			content = strings.TrimSpace(msg.Content)
		}
		if stamped := l.Deps.ConfirmVerifyResult(sess, content); stamped != "" {
			msg.ToolResult.Content = stamped
		}
	}
	if !eligible {
		return nil
	}
	content := msg.Content
	if msg.ToolResult != nil && strings.TrimSpace(msg.ToolResult.Content) != "" {
		content = msg.ToolResult.Content
	}
	handle := ""
	patchedContent := content
	if l.Deps.CommitEvidenceToolResult != nil && sess != nil {
		artifactID := ""
		if msg.ToolResult != nil && msg.ToolResult.Visual != nil {
			artifactID = msg.ToolResult.Visual.ID
		}
		var err error
		handle, patchedContent, err = l.Deps.CommitEvidenceToolResult(ctx, sessionID, sess, toolName, args, content, artifactID)
		if err != nil {
			return fmt.Errorf("record %s evidence: %w", toolName, err)
		}
	}
	content = patchedContent
	if handle == "" {
		return nil
	}
	// Re-stamp artifact_id after evidence rewrites the JSON.
	if msg.ToolResult != nil && msg.ToolResult.Visual != nil {
		content = visual.StampArtifactID(content, msg.ToolResult.Visual.ID)
	}
	msg.Content = guidance.PrependHandleTag(content, handle)
	if msg.ToolResult != nil {
		msg.ToolResult.Content = guidance.PrependHandleTag(content, handle)
		if msg.ToolResult.Visual != nil {
			msg.ToolResult.Visual.EvidenceHandle = handle
		}
		// Evidence handles do not change structured outcome fields.
	}
	msg.EvidenceHandles = append(append([]string(nil), msg.EvidenceHandles...), handle)
	return nil
}

// stampDietFieldsOnCommit sets durable Message diet stamps from machine producers.
func stampDietFieldsOnCommit(toolName string, msg *api.Message) {
	if msg == nil {
		return
	}
	content := msg.Content
	if msg.ToolResult != nil && strings.TrimSpace(msg.ToolResult.Content) != "" {
		content = msg.ToolResult.Content
	}
	if !tooloutput.IsOverlayPromoteTool(toolName) {
		return
	}
	if compaction.IsOverlayPromoteConflictProtected(content) {
		msg.DietStamp = compaction.DietStampPreserveStructure
		msg.DietStampSource = compaction.DietStampSourceOverlayMerge
	}
}

// compactToolWireOnCommit compacts payloads after evidence handles are minted.
func (l *PromptLoop) compactToolWireOnCommit(ctx context.Context, sess *api.Session, toolName string, msg *api.Message) {
	if l == nil || msg == nil || l.Deps.CompactToolWire == nil {
		return
	}
	stampDietFieldsOnCommit(toolName, msg)
	content := msg.Content
	if msg.ToolResult != nil && strings.TrimSpace(msg.ToolResult.Content) != "" {
		content = msg.ToolResult.Content
	}
	out, meta := l.Deps.CompactToolWire(ctx, sess, toolName, content, compaction.CompactToolWireOpts{
		DietStamp:       msg.DietStamp,
		DietStampSource: msg.DietStampSource,
		EvidenceHandles: append([]string(nil), msg.EvidenceHandles...),
	})
	msg.Content = out
	if msg.ToolResult != nil {
		msg.ToolResult.Content = out
	}
	msg.CompactedChunk = meta
}

// retrievalSourceLabel formats host-observed retrieval attribution.
func retrievalSourceLabel(tool, observed string) string {
	label := strings.TrimSpace(tool)
	if label == "" {
		label = "retrieval"
	}
	// Redirects use only the handler's observed destination.
	detail := strings.TrimSpace(observed)
	if detail == "" {
		return label
	}
	return label + " · " + detail
}
