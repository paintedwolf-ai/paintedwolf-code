package promptloop

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

// toolInvocations invokes one tool call and turns its result or refusal into transcript rows.
type toolInvocations struct{ *PromptLoop }

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

func (l toolInvocations) executeToolCall(
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
		return toolBatch(l).runHeldToolCall(ctx, sess, tc, toolCtx, heldToolCall{
			receipt: receipt, contract: def.Contract, argsDigest: argsDigest, run: runTool,
		})
	}
	return runTool(ctx)
}

func (l toolInvocations) finalizeToolRun(ctx context.Context, sess *api.Session, run completedToolRun) (result toolInvocation) {
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
				_ = turnNudges(l).recordDoomLoopAttempt(ctx, run.sessionID, run.responseID, run.call.Name, run.call.Args, "", run.contract.MutatesWorld())
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
	turnNudges(l).recordSearchOutcome(ctx, run.sessionID, run.call.Name, run.call.Args, run.output)
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

func (l toolInvocations) settleInvocation(
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
func (l toolInvocations) recordSourceRunEvidence(
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
