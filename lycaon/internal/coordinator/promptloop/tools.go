package promptloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	"github.com/lycaon/lycaon/internal/toolscope"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/webresearch"
	"github.com/lycaon/lycaon/pkg/api"
)

var spillWireLog = observability.LazyComponent("tool_spill")

var ownerFailureLog = observability.LazyComponent("tool_owner")

var ledgerUnwiredOnce sync.Once

// warnLedgerUnwired reports a missing recorder once without failing the call.
func warnLedgerUnwired() {
	ledgerUnwiredOnce.Do(func() {
		ownerFailureLog.Warn("invocation recorder not wired: tool calls settle with no ledger row")
	})
}

// toolCaptures holds per-invocation UI metadata.
type toolCaptures struct {
	sourceContext    *api.SourceContext
	overlayPromotion *api.OverlayPromotion
	ownerRef         string
	fileEdit         *tools.FileEditCapture
	visual           *tools.VisualCapture
	note             *tools.AgentNoteCapture
	externalAccess   *api.ExternalAccess
	displaySubject   string
	skill            *api.SkillActivation
	process          *api.ToolProcessHandle
	verdict          *api.VerdictOutcome
	sourceRun        *tools.SourceRunCapture
	dispatch         *api.WorkerDispatch
	completion       *api.ToolCompletion
	// retrievedFrom is the handler-observed retrieval host.
	retrievedFrom string
	// sourceReads is the project text the handler returned.
	sourceReads []agentpresence.Read
}

// toolResultProjection keeps result facts separate from display text.
type toolResultProjection struct {
	reject  *tools.ToolReject
	content string
	facts   guidance.ToolResultFacts
	// limit records how session output limits cut what the model receives.
	limit agentpresence.OutputLimit
}

func (p toolResultProjection) withCode(code string) toolResultProjection {
	p.facts = p.facts.WithCode(code)
	return p
}

// toolInvocation holds one settled call and its side outputs.
// invoked is true only after reaching the subsystem owner.
type toolInvocation struct {
	content string
	facts   guidance.ToolResultFacts
	reject  *guidance.Refusal
	invoked bool
	// hostAnswered marks a result produced before the subsystem owner ran.
	hostAnswered     bool
	receipt          *api.InvocationReceipt
	contract         toolcontract.Contract
	captures         toolCaptures
	failure          *api.InvocationFailure
	sourceRevision   string
	sourceRootDigest string
}

// [OAR-OPS-8] Parallel captures cannot deliver content withheld by policy.
func (t *toolInvocation) replaceContent(content string) {
	t.content = content
	t.facts.ContentReplaced = true
	t.captures = toolCaptures{}
	for i := range t.facts.Feedback {
		t.facts.Feedback[i].Details = nil
	}
	if t.failure != nil {
		t.failure.Details = nil
	}
}

func (t toolInvocation) succeeded() bool { return t.facts.Succeeded() }

// refusedBy replaces this result with a host refusal of it.
func (t toolInvocation) refusedBy(reject *guidance.Refusal) toolInvocation {
	if reject == nil {
		return t
	}
	t.content = reject.Body
	// The final refusal selects the card; earlier observations remain attached.
	t.facts = reject.Facts.Merge(t.facts)
	t.reject = reject
	t.captures.note = nil
	return t
}

// asReject returns the settled refusal body and facts.
func (t toolInvocation) asReject() *guidance.Refusal {
	out := &guidance.Refusal{Body: t.content, Facts: t.facts}
	if t.reject != nil {
		out.Copy = t.reject.Copy
	}
	return out
}

// failedInvocation is a call the registry ran and that errored.
func failedInvocation(content string, captures toolCaptures) toolInvocation {
	return toolInvocation{
		content:  content,
		facts:    guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeError},
		captures: captures,
	}
}

func ownerFailureFromError(err error, tool string, contract toolcontract.Contract, captures toolCaptures) *api.InvocationFailure {
	ownerRef := strings.TrimSpace(captures.ownerRef)
	if ownerRef == "" {
		ownerRef = strings.TrimSpace(contract.Owner)
	}
	if errors.Is(err, context.Canceled) {
		return &api.InvocationFailure{Code: tools.ToolOwnerInterruptedCode, Class: "interrupted", Retryable: true, OwnerRef: ownerRef}
	}
	if reject := tools.AsToolReject(err); reject != nil {
		reject = tools.CompleteFailureMetadata(reject, tool, ownerRef)
		return &api.InvocationFailure{
			Code: reject.Code, Class: reject.FailureClass,
			Retryable: reject.Retryable, OwnerRef: reject.OwnerRef, Details: reject.Data,
		}
	}
	// Log unstructured subsystem-owner errors.
	ownerFailureLog.Warn("tool subsystem owner failed without a structured reject",
		"tool", tool, "owner_ref", ownerRef, "error", err)
	return &api.InvocationFailure{
		Code: tools.ToolOwnerFailedCode, Class: "owner_error", Retryable: false, OwnerRef: ownerRef,
		Details: map[string]any{"reason": err.Error()},
	}
}

// statedOrOwnerFailure supplies ToolOwnerFailedCode when the subsystem owner stated none.
func statedOrOwnerFailure(facts guidance.ToolResultFacts, contract toolcontract.Contract, captures toolCaptures) *api.InvocationFailure {
	ownerRef := invocationFailureOwner(contract, captures)
	if code := strings.TrimSpace(facts.PrimaryCode()); code != "" {
		feedback := facts.FeedbackFor(code)
		return &api.InvocationFailure{
			Code: code, Class: "host_rejection", Retryable: true, OwnerRef: ownerRef, Details: feedback.Details,
		}
	}
	return &api.InvocationFailure{
		Code: tools.ToolOwnerFailedCode, Class: "owner_error", Retryable: false, OwnerRef: ownerRef,
	}
}

func rejectionFailure(code, class, ownerRef string, details ...map[string]any) *api.InvocationFailure {
	if strings.TrimSpace(code) == "" {
		code = "TOOL_REJECTED"
	}
	var data map[string]any
	if len(details) > 0 {
		data = details[0]
	}
	return &api.InvocationFailure{
		Code: code, Class: class, Retryable: false, OwnerRef: strings.TrimSpace(ownerRef), Details: data,
	}
}

func invocationFailureOwner(contract toolcontract.Contract, captures toolCaptures) string {
	if ownerRef := strings.TrimSpace(captures.ownerRef); ownerRef != "" {
		return ownerRef
	}
	return strings.TrimSpace(contract.Owner)
}

// refusedInvocation carries structured refusal facts.
func refusedInvocation(reject *guidance.Refusal) toolInvocation {
	return toolInvocation{content: reject.Body, facts: reject.Facts, reject: reject}
}

type completedToolRun struct {
	responseID   string
	sessionID    string
	call         api.ToolCall
	toolCtx      tools.ToolContext
	receipt      *api.InvocationReceipt
	contract     toolcontract.Contract
	output       string
	runErr       error
	startedAt    time.Time
	doomPreCount int
}

func (l *PromptLoop) executeToolCall(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	userPrompt string,
	history []api.Message,
	tc api.ToolCall,
	toolCtx tools.ToolContext,
	taskAllowlist []string,
	doomPreCount int,
	assistantMessageID string,
	_ api.CoordinatorRunContext,
) toolInvocation {
	ctx = tools.WithRecoveryTools(ctx, toolCtx.TurnOfferedToolNames)
	// A non-nil roster constrains task spawning.
	if taskAllowlist != nil {
		ctx = toolpolicy.WithTaskSpawnAllowlist(ctx, taskAllowlist)
	}
	ctx = curationctx.WithTaskHint(ctx, userPrompt)
	ctx = curationctx.WithSession(ctx, curationctx.Session{
		SessionID:       sessionID,
		ProjectID:       sess.ProjectID,
		OwnerPersonID:   sess.OwnerPersonID,
		Posture:         string(sess.Posture),
		Agent:           toolCtx.Agent,
		ParentSessionID: toolCtx.ParentSessionID,
		ToolCallID:      tc.ID,
		ProjectDir:      toolCtx.ActiveRootPath(),
	})
	toolCtx.Out = &tools.ToolInvocationOut{}
	toolCtx.ArgsTruncated = tc.ArgsTruncated
	toolCtx.ArgsMalformed = tc.ArgsMalformed
	toolCtx.ToolCallID = tc.ID
	if toolCtx.TurnOfferedToolNames != nil && !slices.Contains(toolCtx.TurnOfferedToolNames, tc.Name) {
		return refusedInvocation(l.rejectToolOccurrence(ctx, sess, tc, toolCtx, "TOOL_NOT_OFFERED", nil))
	}
	// The active turn compile determines whether the definition is reachable.
	if !surfaceAllowsTool(toolCtx.TurnToolPlan, toolCtx.TurnSurfaceID, tc.Name) {
		return refusedInvocation(l.rejectToolOccurrence(ctx, sess, tc, toolCtx, offSurfaceCode(tc.Name), nil))
	}
	if l.Deps.Tools == nil {
		out := failedInvocation("", toolCaptures{})
		out.facts = out.facts.WithCode(tools.ToolOwnerFailedCode)
		return out
	}
	def, ok := l.Deps.Tools.Definition(tc.Name)
	if !ok {
		return refusedInvocation(l.rejectToolOccurrence(ctx, sess, tc, toolCtx, tools.ToolOwnerFailedCode, nil))
	}
	argsDigest, err := invocation.ArgsDigest(tc.Args)
	if err != nil {
		return toolInvocation{content: err.Error(), facts: guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeError}.WithCode(tools.ToolOwnerFailedCode)}
	}
	// Open the ledger row before any pre-invoke outcome.
	invocationID := uuid.NewString()
	var receipt *api.InvocationReceipt
	if l.Deps.Invocations == nil {
		warnLedgerUnwired()
	} else {
		receipt, err = l.Deps.Invocations.Begin(ctx, invocation.Start{
			ProjectID: sess.ProjectID, SessionID: sessionID,
			AssistantMessageID: assistantMessageID, ToolCallID: tc.ID,
			ToolName: tc.Name, Args: tc.Args, Contract: def.Contract,
		})
		if err != nil {
			return toolInvocation{content: err.Error(), facts: guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeError}.WithCode(tools.ToolOwnerFailedCode)}
		}
		invocationID = receipt.ID
	}
	schema := def.Meta.ArgsSchema
	if offered, ok := toolCtx.TurnOfferedToolSchemas[tc.Name]; ok {
		schema = offered
	}
	if reject := tools.ValidateCallArguments(tc.Name, tc.Args, schema, toolCtx); reject != nil {
		refused := refusedInvocation(l.toolReject(reject.Code, reject.Data))
		refused.receipt, refused.contract = receipt, def.Contract
		return refused
	}
	if l.Deps.BeforeToolRun != nil {
		if out, skip, err := l.Deps.BeforeToolRun(ctx, sess, history, userPrompt, tc.Name, tc.Args); err != nil {
			refused := refusedInvocation(rejectForCallError(err))
			refused.receipt, refused.contract = receipt, def.Contract
			return refused
		} else if skip {
			// The hook answered before the subsystem owner ran.
			return toolInvocation{
				content: out, receipt: receipt, contract: def.Contract, hostAnswered: true,
			}
		}
	}
	toolCtx.Invocation = tools.Invocation{
		ID: invocationID, ProjectID: sess.ProjectID, SessionID: sessionID,
		MessageID: assistantMessageID, ToolCallID: tc.ID, ToolName: tc.Name,
		ArgsDigest: argsDigest, ContractDigest: def.Contract.Digest(), Contract: def.Contract,
	}
	activity := l.beginActivity(ctx, sess, sessionID, api.ActivityKindRunningTool, tc.Name, tc.ID)
	defer activity.finish()
	toolCtx.ReportProgress = activity.report
	// A held call keeps its presence until it settles, under its own context.
	runTool := func(runCtx context.Context) toolInvocation {
		runToolCtx := toolCtx
		if presence := l.Deps.AgentPresence; presence != nil {
			call := agentpresence.Call{SessionID: sessionID, ToolCallID: tc.ID, Tool: tc.Name}
			runToolCtx.Presence = callPresence{ctx: runCtx, tracker: presence, call: call}
			defer presence.CallEnded(runCtx, call)
		}
		start := time.Now()
		output, runErr := l.Deps.Tools.Run(runCtx, tc.Name, tc.Args, runToolCtx)
		return l.finalizeToolRun(runCtx, sess, completedToolRun{
			sessionID: sessionID, responseID: assistantMessageID, call: tc, toolCtx: runToolCtx,
			receipt: receipt, contract: def.Contract, output: output, runErr: runErr,
			startedAt: start, doomPreCount: doomPreCount,
		})
	}
	if def.Contract.DetachAfterBudget && l.Deps.HeldCalls != nil {
		return l.runHeldToolCall(ctx, sess, tc, toolCtx, heldToolCall{
			receipt: receipt, contract: def.Contract, argsDigest: argsDigest, run: runTool,
		})
	}
	return runTool(ctx)
}

func (l *PromptLoop) finalizeToolRun(ctx context.Context, sess *api.Session, run completedToolRun) (result toolInvocation) {
	ownerInvoked := run.toolCtx.Out != nil && run.toolCtx.Out.OwnerInvoked
	captures := toolCapturesFrom(run.toolCtx.Out)
	toolContent := run.output
	defer func() {
		if run.toolCtx.Out != nil {
			result.facts = result.facts.Merge(run.toolCtx.Out.Facts)
		}
		// [OAR-PROF-10] Every returned result, including failures, crosses policy before logging or delivery.
		if ownerInvoked || run.runErr == nil {
			if l.Deps.EnrichToolOutput != nil {
				content, facts := l.Deps.EnrichToolOutput(ctx, sess, run.call.Name, run.call.Args,
					result.content, result.facts, run.doomPreCount+1)
				result.content, result.facts = content, facts
			}
			if result.facts.ContentReplaced {
				result.replaceContent(result.content)
			}
			if run.runErr == nil && result.facts.Succeeded() {
				_ = l.recordDoomLoopAttempt(ctx, run.sessionID, run.responseID, run.call.Name, run.call.Args, "", run.contract.MutatesWorld())
			}
		}
		observability.LogToolInvocation(observability.ToolInvocationCapture{
			SessionID: run.sessionID, Tool: run.call.Name, Profile: run.toolCtx.Agent,
			Duration: time.Since(run.startedAt), Output: result.content, Succeeded: result.facts.Succeeded(),
		})
	}()
	if run.runErr != nil {
		content := run.runErr.Error()
		if trimmed := strings.TrimSpace(run.output); trimmed != "" {
			content = trimmed + "\n" + content
			if l.Deps.AfterToolRun != nil && tooloutput.IsOverlayPromoteTool(run.call.Name) {
				content = l.Deps.AfterToolRun(ctx, sess, run.call.Name, run.call.Args, content, false, run.toolCtx.Out)
			}
		}
		// Typed refusals retain their structured facts.
		if reject, ok := guidance.RefusalFromError(run.runErr); ok {
			out := refusedInvocation(reject)
			out.content = content
			out.invoked = ownerInvoked
			out.captures = captures
			out.receipt, out.contract = run.receipt, run.contract
			if tools.AsToolReject(run.runErr) != nil {
				out.failure = ownerFailureFromError(run.runErr, run.call.Name, run.contract, captures)
				out.failure.Code = reject.Code()
			} else {
				out.failure = rejectionFailure(reject.Code(), "policy_rejection", invocationFailureOwner(run.contract, captures), reject.Facts.FeedbackFor(reject.Code()).Details)
			}
			return out
		}
		out := failedInvocation(content, captures)
		out.invoked = ownerInvoked
		out.receipt, out.contract = run.receipt, run.contract
		out.failure = ownerFailureFromError(run.runErr, run.call.Name, run.contract, captures)
		if out.failure != nil {
			out.facts = out.facts.WithFeedback(out.failure.Code, out.failure.Details, nil)
		}
		return out
	}
	if l.Deps.AfterToolRun != nil {
		toolContent = l.Deps.AfterToolRun(ctx, sess, run.call.Name, run.call.Args, toolContent, true, run.toolCtx.Out)
	}
	// Lifecycle hooks contribute facts before this merge.
	facts := guidance.ToolResultFacts{}
	if run.toolCtx.Out != nil {
		facts = facts.Merge(run.toolCtx.Out.Facts)
	}
	// Record search outcomes before enrichment changes structured output.
	l.recordSearchOutcome(ctx, run.sessionID, run.call.Name, run.call.Args, run.output)
	return toolInvocation{
		content:  toolContent,
		facts:    facts,
		invoked:  ownerInvoked,
		receipt:  run.receipt,
		contract: run.contract,
		captures: captures,
	}
}

func toolCapturesFrom(out *tools.ToolInvocationOut) toolCaptures {
	if out == nil {
		return toolCaptures{}
	}
	return toolCaptures{
		displaySubject:   out.DisplaySubject,
		overlayPromotion: out.OverlayPromotion,
		sourceContext:    out.SourceContext,
		ownerRef:         out.OwnerRef, fileEdit: out.FileEdit, visual: out.Visual,
		note: out.AgentNote, externalAccess: out.ExternalAccess,
		skill: out.Skill, process: out.Process, verdict: out.Verdict,
		sourceRun: out.SourceRun, dispatch: out.Dispatch, completion: out.Completion,
		retrievedFrom: out.RetrievedFrom,
		sourceReads:   out.SourceReads,
	}
}

func (l *PromptLoop) settleInvocation(
	ctx context.Context,
	run toolInvocation,
	status api.InvocationStatus,
	evidenceKind, evidenceRef, ownerRef string,
) (toolInvocation, error) {
	if run.receipt == nil || l.Deps.Invocations == nil {
		return run, nil
	}
	receipt, err := l.Deps.Invocations.Settle(context.WithoutCancel(ctx), run.receipt.ID, invocation.Settlement{
		Status: status, Invoked: run.invoked, EvidenceKind: evidenceKind,
		EvidenceRef: evidenceRef, OwnerRef: ownerRef, Failure: run.failure,
		Isolation:      invocationIsolationOf(run),
		SourceRevision: run.sourceRevision, SourceRootDigest: run.sourceRootDigest,
		SourceVerdict: sourceVerdictOf(run.captures.sourceRun),
	})
	if err != nil {
		return run, err
	}
	run.receipt = receipt
	return run, nil
}

func invocationIsolationOf(run toolInvocation) *api.InvocationIsolation {
	if run.failure != nil {
		if outcome, ok := isolation.Lookup(strings.TrimSpace(run.failure.Code)); ok {
			return &api.InvocationIsolation{Code: outcome.Code, Disposition: string(outcome.Disposition)}
		}
	}
	for _, code := range run.facts.Codes {
		if outcome, ok := isolation.Lookup(strings.TrimSpace(code)); ok {
			return &api.InvocationIsolation{Code: outcome.Code, Disposition: string(outcome.Disposition)}
		}
	}
	return nil
}

// recordSourceRunEvidence appends the subsystem owner's run to session evidence.
func (l *PromptLoop) recordSourceRunEvidence(
	ctx context.Context,
	sessionID string,
	sess *api.Session,
	tool string,
	run toolInvocation,
) {
	if l.Deps.RecordSourceRunEvidence == nil || run.captures.sourceRun == nil || !run.succeeded() {
		return
	}
	l.Deps.RecordSourceRunEvidence(context.WithoutCancel(ctx), sessionID, sess, tool, *run.captures.sourceRun)
}

// sourceVerdictOf reads the subsystem owner's verdict.
func sourceVerdictOf(run *tools.SourceRunCapture) string {
	if run == nil {
		return ""
	}
	return strings.TrimSpace(run.Verdict)
}

func invocationOwnerRef(explicit string, result *api.ToolResult) string {
	if ref := strings.TrimSpace(explicit); ref != "" {
		return ref
	}
	if result == nil {
		return ""
	}
	if result.Dispatch != nil {
		for _, ref := range []string{result.Dispatch.WorkerID, result.Dispatch.ChildSessionID} {
			if strings.TrimSpace(ref) != "" {
				return strings.TrimSpace(ref)
			}
		}
	}
	if result.Process != nil {
		return strings.TrimSpace(result.Process.Handle)
	}
	if result.Visual != nil {
		return strings.TrimSpace(result.Visual.ID)
	}
	return ""
}

func applyToolResultSidecars(
	ctx context.Context,
	store visual.Store,
	designateCover func(ctx context.Context, projectID, rootSessionID, artifactID string) error,
	projectID string,
	rootSessionID string,
	producerSessionID string,
	toolName, toolContent string,
	tr *api.ToolResult,
	captures toolCaptures,
) string {
	guidance.ApplyTaskDispatchMetadata(toolName, tr, captures.dispatch)
	tr.DisplaySubject = captures.displaySubject
	tr.OverlayPromotion = captures.overlayPromotion
	if captures.completion != nil {
		completion := *captures.completion
		tr.Completion = &completion
	}
	if fileEdit := captures.fileEdit; fileEdit != nil {
		tr.FileEdit = &api.FileEditSnapshot{
			Path:   fileEdit.Path,
			Before: fileEdit.Before,
			After:  fileEdit.After,
		}
	}
	if captures.externalAccess != nil {
		tr.ExternalAccess = tools.ExternalAccessFromCapture(captures.externalAccess)
	}
	if captures.skill != nil {
		tr.Skill = captures.skill
	}
	if captures.process != nil {
		tr.Process = captures.process
		if captures.process.Running {
			// A live handle stays visible until the mirror reports it settled.
			tr.UiVisibility = api.ToolResultUiVisibilityNormal
		}
	}
	if captures.verdict != nil {
		tr.Verdict = captures.verdict
	}
	if visualCap := captures.visual; visualCap != nil && store != nil && strings.TrimSpace(rootSessionID) != "" {
		stamped, err := visual.AttachToolResult(ctx, store, rootSessionID, producerSessionID, tr.ToolCallID, tr, visualCap, toolContent)
		if err != nil {
			return toolContent
		}
		toolContent = stamped
		// Visual artifacts stay visible when textual output is benign.
		if tr.Visual != nil {
			tr.UiVisibility = api.ToolResultUiVisibilityNormal
		}
		if tr.Visual != nil && designateCover != nil && strings.TrimSpace(projectID) != "" && isCoverProducerTool(toolName) {
			src := tr.Visual.Source
			if (src == api.VisualArtifactSourceCapture || src == api.VisualArtifactSourceRender) &&
				visual.IsRasterMime(tr.Visual.Mime) {
				_ = designateCover(ctx, projectID, rootSessionID, tr.Visual.ID)
			}
		}
	}
	return toolContent
}

func isCoverProducerTool(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "capture_page", "render_view":
		return true
	default:
		return false
	}
}

// appendUserNudge defers persistence during an open draft slot.
func (l *PromptLoop) appendUserNudge(
	ctx context.Context,
	sessionID string,
	history []api.Message,
	content string,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	return l.appendHostNudge(ctx, sessionID, history, HostNudge{Content: content}, "", st)
}

func newHostNudge(nudge HostNudge, kind api.MessageKind) api.Message {
	signalID := strings.TrimSpace(nudge.SignalID)
	if nudge.Feedback != nil {
		signalID = feedbackSignalID(*nudge.Feedback)
	}
	return api.Message{
		ID:        uuid.NewString(),
		Role:      api.MessageRoleUser,
		Content:   nudge.Content,
		Origin:    api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		Kind:         kind,
		HostSignalID: signalID,
		Visibility:   api.MessageVisibilityInternal,
		CreatedAt:    time.Now().UTC(),
	}
}

func (l *PromptLoop) appendHostNudge(
	ctx context.Context,
	sessionID string,
	history []api.Message,
	hostNudge HostNudge,
	kind api.MessageKind,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	hostNudge.Content = strings.TrimSpace(hostNudge.Content)
	if hostNudge.Content == "" {
		return history, nil
	}
	if i := lastHostNudgeWithSignal(history, hostNudge); i >= 0 {
		history[i].Content = hostNudge.Content
		history[i].CreatedAt = time.Now().UTC()
		if replaceDeferredUserNudge(st, history[i]) {
			return history, nil
		}
		if l.Deps.UpdateMessage != nil {
			if err := l.Deps.UpdateMessage(ctx, sessionID, history[i].ID, history[i]); err != nil {
				return nil, err
			}
		}
		return history, nil
	}
	nudge := newHostNudge(hostNudge, kind)
	history = append(history, nudge)
	if deferUserNudgeStore(st) {
		st.deferredUserNudges = append(st.deferredUserNudges, nudge)
		return history, nil
	}
	if l.Deps.AppendMessages == nil {
		return nil, fmt.Errorf("append messages not configured")
	}
	if err := l.Deps.AppendMessages(ctx, sessionID, nudge); err != nil {
		return nil, err
	}
	return history, nil
}

func lastHostNudgeWithSignal(history []api.Message, nudge HostNudge) int {
	signalID := strings.TrimSpace(nudge.SignalID)
	if nudge.Feedback != nil {
		signalID = feedbackSignalID(*nudge.Feedback)
	}
	if signalID == "" {
		return -1
	}
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Origin != api.MessageOriginHost {
			continue
		}
		if strings.TrimSpace(msg.HostSignalID) == signalID {
			return i
		}
	}
	return -1
}

func feedbackSignalID(feedback api.ToolFeedback) string {
	code := strings.TrimSpace(feedback.Code)
	if code == "" {
		return ""
	}
	if feedback.Subject == nil {
		return "guidance:" + code
	}
	kind := strings.TrimSpace(feedback.Subject.Kind)
	id := strings.TrimSpace(feedback.Subject.ID)
	if kind == "" || id == "" {
		return "guidance:" + code
	}
	return "guidance:" + code + ":" + kind + ":" + id
}

func replaceDeferredUserNudge(st *promptLoopTurnState, msg api.Message) bool {
	if st == nil {
		return false
	}
	for i := range st.deferredUserNudges {
		if st.deferredUserNudges[i].ID == msg.ID {
			st.deferredUserNudges[i] = msg
			return true
		}
	}
	return false
}

func deferUserNudgeStore(st *promptLoopTurnState) bool {
	return st != nil && st.draftSlotID != "" && st.draftSlotAppended
}

func (l *PromptLoop) flushDeferredUserNudges(ctx context.Context, sessionID string, st *promptLoopTurnState) error {
	if l == nil || st == nil || len(st.deferredUserNudges) == 0 {
		return nil
	}
	if l.Deps.AppendMessages == nil {
		return fmt.Errorf("append messages not configured")
	}
	pending := make([]api.Message, 0, len(st.deferredUserNudges))
	for _, nudge := range st.deferredUserNudges {
		nudge.Content = strings.TrimSpace(nudge.Content)
		if nudge.Content == "" {
			continue
		}
		pending = append(pending, nudge)
	}
	if len(pending) == 0 {
		st.deferredUserNudges = nil
		return nil
	}
	if err := l.Deps.AppendMessages(ctx, sessionID, pending...); err != nil {
		return err
	}
	st.deferredUserNudges = nil
	return nil
}

func (l *PromptLoop) closeCoordinatorDraftSlot(ctx context.Context, sessionID string, st *promptLoopTurnState, messageID string) error {
	if st != nil {
		st.closeCoordinatorDraftSlot(messageID)
	}
	return l.flushDeferredUserNudges(ctx, sessionID, st)
}

// recordSearchOutcome records survey material from structured receipts.
func (l *PromptLoop) recordSearchOutcome(ctx context.Context, sessionID, tool string, args map[string]any, output string) {
	if l.Deps.DoomLoop == nil {
		return
	}
	receipt, ok := surveyreceipt.Parse(output)
	if !ok {
		return
	}
	_, _ = l.Deps.DoomLoop.RecordSearchOutcome(ctx, sessionID, tool, args, receipt.PathsTouched > 0)
}

func (l *PromptLoop) checkDoomLoop(ctx context.Context, sessionID, responseID, tool string, args map[string]any, countOut *int) error {
	if l.Deps.DoomLoop == nil {
		return nil
	}
	// Repeated terminal keystrokes are valid interactive input.
	if strings.EqualFold(strings.TrimSpace(tool), "terminal_send") {
		return nil
	}
	if tools.ToolOffered(ctx, tool) {
		if err := l.Deps.DoomLoop.ResolveRejection(ctx, sessionID, tool, args, "TOOL_NOT_OFFERED"); err != nil {
			return err
		}
	}
	allowed, count, repeatedCode, err := l.Deps.DoomLoop.Check(ctx, sessionID, responseID, tool, args)
	if err != nil {
		return err
	}
	if countOut != nil {
		*countOut = count
	}
	if allowed {
		return nil
	}
	if l.Deps.FormatDoomLoopReject != nil {
		reject, fmtErr := l.Deps.FormatDoomLoopReject(ctx, sessionID, tool, args, count, repeatedCode)
		if fmtErr != nil {
			return fmtErr
		}
		if reject != nil && strings.TrimSpace(reject.Body) != "" {
			return reject
		}
	}
	// Missing enforcement decisions block execution.
	return errors.New("doom loop blocked")
}

// escalateRepeatedCode returns escalation for a repeated rejection code.
func (l *PromptLoop) escalateRepeatedCode(ctx context.Context, sessionID, tool string, original *guidance.Refusal) *guidance.Refusal {
	if l.Deps.EscalateRepeatedCode == nil || original == nil || original.Code() == "" {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(tool), "terminal_send") {
		return nil
	}
	return l.Deps.EscalateRepeatedCode(ctx, sessionID, tool, original)
}

func (l *PromptLoop) recordDoomLoopAttempt(
	ctx context.Context,
	sessionID, responseID, tool string,
	args map[string]any,
	rejectCode string,
	mutated bool,
) error {
	if l.Deps.DoomLoop == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(tool), "terminal_send") {
		return nil
	}
	return l.Deps.DoomLoop.RecordAttempt(ctx, sessionID, responseID, tool, args, rejectCode, mutated)
}

func (l *PromptLoop) webSearchEnabled() bool {
	if l == nil || l.Deps.WebSearchEnabled == nil {
		return true
	}
	return l.Deps.WebSearchEnabled()
}

func surfaceAllowsTool(plan toolsurface.Plan, surfaceID, toolName string) bool {
	surfaceID = strings.TrimSpace(surfaceID)
	// Workers and unconfigured hosts have no coordinator surface compile.
	if surfaceID == "" && !plan.Compiled() {
		return true
	}
	return plan.Compiled() && plan.Addressable(toolName)
}

func offSurfaceCode(toolName string) string {
	if toolcontract.MutatesContent(toolName) {
		return "COORDINATOR_ORCHESTRATE_WRITE_DENIED"
	}
	return "COORDINATOR_TOOL_DENIED"
}

func (l *PromptLoop) coordinatorToolsForTurn(
	ctx context.Context,
	sess *api.Session,
	profileID string,
	history []api.Message,
	userPrompt string,
	iterIndex, maxIter int,
	frame inject.CoordinatorTurnFrame,
	st *promptLoopTurnState,
) ([]tools.ToolMeta, toolsurface.Plan, string, error) {
	if l == nil {
		return nil, toolsurface.Plan{}, "", nil
	}
	if st != nil && st.proseTurn(sess, iterIndex, maxIter) {
		st.proseFinish = true
	}
	surfaceID := ""
	if guard.IsCoordinatorProfile(profileID) {
		surfaceID = l.resolveTurnProfile(ctx, sess, history, frame).SurfaceID
	}
	if l.Deps.Policy == nil {
		return nil, toolsurface.Plan{}, surfaceID, nil
	}
	all := l.Deps.Policy.ListForPrompt(ctx, sess, profileID)
	if frame.Machine.Compiled() {
		all = tools.HideSkillsReadWhenEmpty(all, frame.Machine.SkillCount)
	}
	activated := l.activeDeferredTools(sess)
	if profileID == prompts.CoordinatorProfileID {
		all = l.appendActivatedOpenWorldMetas(all, activated)
	}
	if profileID != prompts.CoordinatorProfileID {
		plan := l.compileWorkerToolPlan(ctx, sess, all, activated)
		filtered := make([]tools.ToolMeta, 0, len(all))
		for _, meta := range all {
			if plan.Immediate(meta.Name) {
				filtered = append(filtered, meta)
			}
		}
		if st != nil && st.proseTurn(sess, iterIndex, maxIter) {
			filtered = workerProseToolMetas(filtered, sess)
			var names []string
			for _, meta := range filtered {
				names = append(names, meta.Name)
			}
			plan = toolsurface.Compile(names, nil)
		}
		return trimPromptToolMetas(filtered), plan, "", nil
	}
	profile := surface.TurnProfile{SurfaceID: surfaceID}
	rootCount := l.projectRootCount(ctx, sess)
	plan, err := surface.CompileToolPlan(profile, rootCount)
	if err != nil {
		return nil, toolsurface.Plan{}, surfaceID, fmt.Errorf("compile coordinator tool surface %q: %w", surfaceID, err)
	}
	taskAllowlist := taskSpawnAllowlistForTurn(frame)
	openWorld := plan.Immediate("request_tools")
	if openWorld {
		all = appendMissingMetas(all, l.liveMCPMetas())
	}
	mcpPlan := l.mcpToolPlan(ctx, sess, all, activated)
	plan = l.compileRuntimeToolPlan(plan, frame.RunContext, all, activated, mcpPlan, openWorld, sess)
	plan = filterToolPlanForRootCount(plan, rootCount)
	return trimCoordinatorToolMetasForPlan(plan, all, taskAllowlist), plan, surfaceID, nil
}

func filterToolPlanForRootCount(plan toolsurface.Plan, rootCount int) toolsurface.Plan {
	if rootCount != 0 {
		return plan
	}
	for _, name := range plan.AddressableNames() {
		if toolscope.RequiresProjectRoots(name) {
			plan = plan.Without(name)
		}
	}
	return plan
}

func trimCoordinatorToolMetasForPlan(plan toolsurface.Plan, all []tools.ToolMeta, taskAllowlist []string) []tools.ToolMeta {
	out := make([]tools.ToolMeta, 0, len(all))
	for _, meta := range all {
		if !plan.Immediate(meta.Name) {
			continue
		}
		outMeta := tools.TrimCoordinatorToolMeta(meta)
		if meta.Name == "task" {
			if taskAllowlist != nil && len(taskAllowlist) == 0 {
				continue
			}
			outMeta = guard.ApplyTaskSpawnAllowlistSchema(outMeta, taskAllowlist)
		}
		out = append(out, outMeta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (l *PromptLoop) compileRuntimeToolPlan(
	plan toolsurface.Plan,
	runCtx api.CoordinatorRunContext,
	metas []tools.ToolMeta,
	activated map[string]bool,
	mcpPlan tools.MCPToolPlan,
	openWorld bool,
	sess *api.Session,
) toolsurface.Plan {
	if strings.TrimSpace(runCtx.AdvanceWhenGateMet) == "coordinator" {
		plan = plan.Promote("workflow_advance")
	}
	if !l.webSearchEnabled() {
		plan = plan.Without(webresearch.SearchToolName, webresearch.FetchURLToolName)
	}
	plan = plan.Promote(toolcontract.Implied(l.liveResources(sess))...)
	for name := range activated {
		if plan.Addressable(name) || !toolcontract.IsCatalog(name) {
			plan = plan.Promote(name)
		}
	}
	if !openWorld {
		return plan
	}
	for _, meta := range metas {
		if !meta.IsMCP() || plan.Addressable(meta.Name) {
			continue
		}
		if mcpPlan.Eager(meta.Name) {
			plan = plan.Promote(meta.Name)
		} else {
			plan = plan.Defer(meta.Name)
		}
	}
	return plan
}

func (l *PromptLoop) mcpToolPlan(ctx context.Context, sess *api.Session, metas []tools.ToolMeta, activated map[string]bool) tools.MCPToolPlan {
	if l != nil && l.Deps.MCPAlwaysLoad != nil {
		modes := l.Deps.MCPAlwaysLoad(ctx, sess)
		resolved := make([]tools.ToolMeta, len(metas))
		copy(resolved, metas)
		for i := range resolved {
			if resolved[i].IsMCP() && strings.TrimSpace(resolved[i].SourceID) != "" {
				if always, ok := modes[resolved[i].SourceID]; ok {
					resolved[i].AlwaysLoad = always
				}
			}
		}
		metas = resolved
	}
	return tools.PlanMCPTools(metas, activated)
}

// appendActivatedOpenWorldMetas adds activated non-catalog tools that ListForPrompt omitted.
func (l *PromptLoop) appendActivatedOpenWorldMetas(all []tools.ToolMeta, activated map[string]bool) []tools.ToolMeta {
	if l == nil || l.Deps.Tools == nil || len(activated) == 0 {
		return all
	}
	have := make(map[string]bool, len(all))
	for _, meta := range all {
		have[meta.Name] = true
	}
	extras := make([]tools.ToolMeta, 0)
	for name := range activated {
		if have[name] || toolcontract.IsCatalog(name) {
			continue
		}
		def, ok := l.Deps.Tools.Definition(name)
		if !ok {
			continue
		}
		extras = append(extras, def.Meta)
	}
	return appendMissingMetas(all, extras)
}

func appendMissingMetas(all, extras []tools.ToolMeta) []tools.ToolMeta {
	if len(extras) == 0 {
		return all
	}
	have := make(map[string]bool, len(all))
	for _, meta := range all {
		have[meta.Name] = true
	}
	add := make([]tools.ToolMeta, 0)
	for _, meta := range extras {
		if have[meta.Name] || strings.TrimSpace(meta.Name) == "" {
			continue
		}
		have[meta.Name] = true
		add = append(add, meta)
	}
	if len(add) == 0 {
		return all
	}
	sort.Slice(add, func(i, j int) bool { return add[i].Name < add[j].Name })
	return append(all, add...)
}

// trimPromptToolMetas applies the provider-safe schema projection.
func trimPromptToolMetas(metas []tools.ToolMeta) []tools.ToolMeta {
	out := make([]tools.ToolMeta, len(metas))
	for i, meta := range metas {
		out[i] = tools.TrimCoordinatorToolMeta(meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (l *PromptLoop) liveResources(sess *api.Session) toolcontract.ResourcePresence {
	if l == nil || l.Deps.LiveResources == nil || sess == nil {
		return toolcontract.ResourcePresence{}
	}
	return l.Deps.LiveResources(sess.ID)
}

func (l *PromptLoop) liveMCPMetas() []tools.ToolMeta {
	if l == nil || l.Deps.Tools == nil {
		return nil
	}
	out := make([]tools.ToolMeta, 0)
	for _, meta := range l.Deps.Tools.List() {
		if meta.IsMCP() {
			out = append(out, meta)
		}
	}
	return out
}

// taskSpawnAllowlistForTurn returns nil for no roster and empty for no agents.
func taskSpawnAllowlistForTurn(frame inject.CoordinatorTurnFrame) []string {
	if frame.Roster == nil {
		return nil
	}
	if frame.Roster.Effective == nil {
		return []string{}
	}
	return frame.Roster.Effective
}

func (l *PromptLoop) projectRootCount(ctx context.Context, sess *api.Session) int {
	if l != nil && l.Deps.ProjectRootCount != nil && sess != nil {
		return l.Deps.ProjectRootCount(ctx, sess)
	}
	if sess != nil && strings.TrimSpace(sess.WorkspacePath) != "" {
		return 1
	}
	return 0
}

func (l *PromptLoop) activeDeferredTools(sess *api.Session) map[string]bool {
	if l == nil || l.Deps.LoadedTools == nil || sess == nil {
		return nil
	}
	return l.Deps.LoadedTools(sess.ID)
}

func (l *PromptLoop) resolveTurnProfile(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	frame inject.CoordinatorTurnFrame,
) surface.TurnProfile {
	implState := l.implementSessionState(ctx, sess)
	return surface.ResolveTurnProfile(frame.RunContext, sess, history, implState)
}

func (l *PromptLoop) implementSessionState(ctx context.Context, sess *api.Session) surface.ImplementSessionState {
	if l == nil || l.Deps.ImplementSessionState == nil || sess == nil {
		return surface.ImplementSessionState{}
	}
	return l.Deps.ImplementSessionState(ctx, sess)
}

// truncateToolResultForSession fits screened tool output to the session wire.
func (l *PromptLoop) truncateToolResultForSession(
	ctx context.Context,
	tool string,
	projection toolResultStorageProjection,
	rawContent string,
	maxBytes, maxSpillBytes int,
	sess *api.Session,
) toolResultProjection {
	hostDataDir := ""
	if sess != nil {
		hostDataDir = project.HostDataDir(l.Deps.DataDir, sess.ProjectID)
		if hostDataDir != "" {
			_, _ = project.EnsureHostDataDir(l.Deps.DataDir, sess.ProjectID)
		}
	}
	durableContent := projection.content
	limit := agentpresence.OutputLimit{}
	if tool == "git_diff" {
		fitted, reject := projectGitDiff(hostDataDir, tooloutput.Screened(durableContent), maxSpillBytes)
		if reject != nil {
			return toolResultProjection{reject: reject}
		}
		if fitted != durableContent {
			limit.Spilled = true
			rawContent = fitted
			durableContent = fitted
		}
	}
	// Clamp after redaction so truncation cannot split a matched secret.
	if clamp, ok := surveyreceipt.ClampSessionToolOutput(durableContent, maxBytes); ok {
		msg := guidance.EnvelopeHintMessage(ctx, l.Deps.HintConfig, "TOOL_SURVEY_BYTE_CLAMPED", clamp.Vars)
		return toolResultProjection{
			content: guidance.AppendOutputBanner(clamp.Output, "TOOL_SURVEY_BYTE_CLAMPED", msg),
			limit:   agentpresence.OutputLimit{KeptEntries: clamp.KeptEntries, KeptThroughLine: clamp.KeptThroughLine},
		}.withCode("TOOL_SURVEY_BYTE_CLAMPED")
	}
	overlayPromote := tooloutput.IsOverlayPromoteTool(tool)
	if overlayPromote {
		maxBytes = tooloutput.OverlayToolResultMaxBytes(maxBytes)
	}
	screened := tooloutput.Screened(durableContent)
	inline := screened
	if overlayPromote {
		if compact, ok := tooloutput.InlineOverlayPromoteJSON(durableContent); ok {
			inline = tooloutput.Screened(compact)
		}
	}

	var out tooloutput.WireSpillOutcome
	if overlayPromote && hostDataDir != "" {
		rel := tooloutput.PromoteSpillRelPath(extractOverlayIDFromToolOutput(durableContent))
		out = tooloutput.WireSpillOverlayPromote(hostDataDir, rel, screened, inline, maxBytes, maxSpillBytes)
	} else {
		out = tooloutput.WireSpillToolOutput(hostDataDir, screened, maxBytes, maxSpillBytes)
	}
	if out.RejectCode != "" {
		rejectData := out.RejectData
		if out.RejectCode == tooloutput.ToolOutputSpillCapExceededCode {
			rejectData = tooloutput.EnrichSpillCapReject(tool, projection.args, durableContent, maxSpillBytes)
		}
		spillWireLog.Info("tool spill rejected at commit",
			"code", out.RejectCode,
			"tool", tool,
			"original_bytes", out.OriginalBytes,
			"reason", rejectData["reason"])
		return toolResultProjection{reject: &tools.ToolReject{Code: out.RejectCode, Data: rejectData}}
	}
	if !out.Truncated {
		// Uncut output preserves the original one-request overlay.
		if maxBytes <= 0 || len(rawContent) <= maxBytes {
			return toolResultProjection{content: rawContent, limit: limit}
		}
		return toolResultProjection{content: out.Preview, limit: limit}
	}
	if out.SpillCapped {
		spillWireLog.Info("tool spill capped",
			"tool", tool,
			"original_bytes", out.OriginalBytes,
			"spill_bytes", out.SpillBytes,
			"spill_path", out.SpillPath)
	} else if out.SpillPath != "" {
		spillWireLog.Info("tool spill written",
			"tool", tool,
			"spill_bytes", out.SpillBytes,
			"spill_path", out.SpillPath)
	}
	hintVars := spillHintVars(out)
	if overlayPromote {
		hintVars["job_id"] = extractOverlayIDFromToolOutput(durableContent)
		hint := guidance.EnvelopeHintMessage(ctx, l.Deps.HintConfig, "OVERLAY_PROMOTE_SPILL", hintVars)
		return toolResultProjection{
			content: guidance.AppendOutputBanner(out.Preview, "OVERLAY_PROMOTE_SPILL", hint),
			limit:   agentpresence.OutputLimit{Spilled: true},
		}.withCode("OVERLAY_PROMOTE_SPILL")
	}
	hintCode := "TOOL_OUTPUT_TRUNCATED"
	if out.SpillCapped {
		hintCode = "TOOL_OUTPUT_SPILL_CAPPED"
		hintVars["cap"] = tooloutput.EffectiveMaxSpillFileBytes(maxSpillBytes)
	}

	hint := guidance.EnvelopeHintMessage(ctx, l.Deps.HintConfig, hintCode, hintVars)
	return toolResultProjection{
		content: guidance.AppendOutputBanner(out.Preview, hintCode, hint),
		limit:   agentpresence.OutputLimit{Spilled: true},
	}.withCode(hintCode)
}

func spillHintVars(out tooloutput.WireSpillOutcome) map[string]any {
	return map[string]any{
		"spill_path":     out.SpillPath,
		"spill_bytes":    out.SpillBytes,
		"original_bytes": out.OriginalBytes,
		"max_file_bytes": readcaps.MaxFileBytes,
		"spill_readable": out.SpillPath != "" && out.SpillBytes <= readcaps.MaxFileBytes,
	}
}

func extractOverlayIDFromToolOutput(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	for i := len(content) - 1; i >= 0; i-- {
		if content[i] != '{' {
			continue
		}
		var payload struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal([]byte(content[i:]), &payload); err != nil {
			continue
		}
		if id := strings.TrimSpace(payload.JobID); id != "" {
			return id
		}
	}
	return ""
}

func (l *PromptLoop) reloadHistoryAfterToolCompaction(ctx context.Context, sessionID string, sess *api.Session, surfaceID string, history []api.Message, st *promptLoopTurnState) ([]api.Message, error) {
	if l.Deps.CompactOversizedToolResults != nil {
		if err := l.Deps.CompactOversizedToolResults(ctx, sessionID, sess); err != nil {
			return history, err
		}
	}
	if l.Deps.ReloadHistory == nil {
		return history, nil
	}
	reloaded, err := l.Deps.ReloadHistory(ctx, sessionID, sess, surfaceID)
	if err != nil {
		return history, err
	}
	return st.applySecretStorageOverlays(reloaded), nil
}
