package promptloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

// modelTurn prepares and streams one model request: the offered tools, the request, and its usage.

func (l *modelTurn) runAssistantStreamTurn(
	ctx context.Context,
	sessionID string,
	sess *api.Session,
	history []api.Message,
	st *promptLoopTurnState,
	profileID string,
	userPrompt string,
	iterIndex, maxIter int,
	hostTurn bool,
) (api.Message, *modelcall.Completion, []string, error) {
	if st != nil && st.attemptID != "" && l.Projection.Deps.AdmitModelResponse != nil {
		if err := l.Projection.Deps.AdmitModelResponse(ctx, sessionID, st.attemptID); err != nil {
			return api.Message{}, nil, nil, err
		}
	}
	messageID := uuid.NewString()
	if st != nil && coordinatorDraftSlotEligible(sess, profileID) {
		messageID = st.ensureCoordinatorDraftSlot()
	}
	outputID := uuid.NewString()
	assistantMsg := newProvisionalAssistantMessage(api.Message{
		ID:        messageID,
		CreatedAt: time.Now().UTC(),
	})
	if coordinatorDraftSlotEligible(sess, profileID) {
		assistantMsg.DraftStatus = api.DraftStatusLive
	}
	if l.Projection.Deps.AppendMessages == nil {
		return api.Message{}, nil, nil, fmt.Errorf("append messages not configured")
	}
	var clearActive sync.Once
	clearStream := func() {
		clearActive.Do(func() {
			if l.Projection.Deps.Streams != nil {
				l.Projection.Deps.Streams.Finish(ctx, sessionID)
			}
		})
	}
	defer clearStream()
	var placeholder struct {
		mu       sync.Mutex
		appended bool
	}
	withdrawFailedDraft := func(cause error) error {
		placeholder.mu.Lock()
		appended := placeholder.appended
		placeholder.mu.Unlock()
		if !appended || !coordinatorDraftSlotEligible(sess, profileID) {
			return cause
		}
		clearStream()
		withdrawErr := l.Nudges.withdrawCoordinatorDraft(
			context.WithoutCancel(ctx), sess, sessionID, st, "",
		)
		return errors.Join(cause, withdrawErr)
	}
	persistPlaceholder := func() error {
		placeholder.mu.Lock()
		defer placeholder.mu.Unlock()
		if placeholder.appended {
			return nil
		}
		if st != nil && st.draftSlotAppended && st.usesCoordinatorDraftSlot(assistantMsg.ID) {
			placeholder.appended = true
			return nil
		}
		if err := l.Projection.Deps.AppendMessages(ctx, sessionID, assistantMsg); err != nil {
			if errors.Is(err, store.ErrDuplicateMessageID) && st != nil && st.usesCoordinatorDraftSlot(assistantMsg.ID) {
				placeholder.appended = true
				st.draftSlotAppended = true
				return nil
			}
			return err
		}
		placeholder.appended = true
		if st != nil && st.usesCoordinatorDraftSlot(assistantMsg.ID) {
			st.draftSlotAppended = true
		}
		return nil
	}
	wireContent := closeoutWireContent(l.Context.promptTurnSurface(sessionID))
	completion, tokens, err := l.completeStream(ctx, sess, sessionID, history, profileID, userPrompt, iterIndex, maxIter, hostTurn, st, func(partial *modelcall.Completion) {
		if partial == nil {
			return
		}
		// [OAR-OPS-8] Content and tool-call deltas remain private until model.output resolves.
		if l.Projection.Deps.Streams != nil {
			generating := tokenest.EstimateDefault(partial.Content) + tokenest.EstimateDefault(partial.Reasoning)
			l.Projection.Deps.Streams.CacheLive(sessionID, assistantMsg.ID, "", nil, generating)
		}
		_ = persistPlaceholder()
	})
	if err != nil {
		return api.Message{}, nil, nil, withdrawFailedDraft(err)
	}
	if err := persistPlaceholder(); err != nil {
		return api.Message{}, nil, nil, err
	}
	clearStream()
	promptAssistant, err := l.Projection.settleAssistantStreamTurn(
		ctx, sessionID, st, assistantMsg, completion, tokens, outputID, iterIndex, wireContent,
	)
	if err != nil {
		return api.Message{}, nil, nil, withdrawFailedDraft(err)
	}
	if st != nil && st.observePrompt != nil {
		st.observePrompt(st.coordinatorFrame)
		st.observePrompt = nil
	}
	return promptAssistant, completion, tokens, nil
}

func (l *turnProjection) settleAssistantStreamTurn(
	ctx context.Context,
	sessionID string,
	st *promptLoopTurnState,
	assistantMsg api.Message,
	completion *modelcall.Completion,
	tokens []string,
	outputID string,
	iterIndex int,
	wireContent func(string) string,
) (api.Message, error) {
	assistantMsg.Content = completion.Content
	assistantMsg.SourceContext = sourceref.Mentioned(completion.SourceContext, completion.Content)
	assistantMsg.ModelReasoning = completion.ModelReasoning()
	assistantMsg.ToolCalls = sanitizeToolCallsForExecution(completion.ToolCalls)
	if len(assistantMsg.ToolCalls) == 0 {
		assistantMsg.ToolCalls = []api.ToolCall{}
	}
	completion.ToolCalls = assistantMsg.ToolCalls
	storedAssistant, transientAssistant := l.storageSafeMessage(ctx, assistantMsg)
	wireAssistant := storedAssistant
	wireAssistant.Content = wireContent(storedAssistant.Content)
	canonicalOutputID := ""
	if st != nil && st.attemptID != "" && l.Deps.SettleModelOutput != nil {
		callsJSON, marshalErr := json.Marshal(wireAssistant.ToolCalls)
		if marshalErr != nil {
			return api.Message{}, marshalErr
		}
		reasoningJSON, marshalErr := json.Marshal(wireAssistant.ModelReasoning)
		if marshalErr != nil {
			return api.Message{}, marshalErr
		}
		committed, settleErr := l.Deps.SettleModelOutput(ctx, store.ModelOutput{
			ID: outputID, TurnAttemptID: st.attemptID, SessionID: sessionID,
			Iteration: iterIndex + 1, MessageID: assistantMsg.ID,
			ProviderID: completion.ProviderID, Model: completion.Model, Scripted: completion.Scripted,
			Content: wireAssistant.Content, ToolCallsJSON: string(callsJSON),
			ReasoningJSON: string(reasoningJSON), CreatedAt: assistantMsg.CreatedAt,
		})
		if settleErr != nil {
			return api.Message{}, settleErr
		}
		st.lastOutputID = committed.ID
		canonicalOutputID = committed.ID
	}
	if l.Deps.UpdateMessage != nil {
		if err := l.Deps.UpdateMessage(ctx, sessionID, assistantMsg.ID, wireAssistant); err != nil {
			if canonicalOutputID == "" {
				return api.Message{}, err
			}
			slog.WarnContext(ctx, "settled model output projection deferred",
				"session_id", sessionID, "message_id", assistantMsg.ID,
				"model_output_id", canonicalOutputID, "error", err)
		} else if canonicalOutputID != "" && l.Deps.MarkModelOutputProjected != nil {
			if err := l.Deps.MarkModelOutputProjected(ctx, canonicalOutputID); err != nil {
				slog.WarnContext(ctx, "model output projection acknowledgement deferred",
					"session_id", sessionID, "model_output_id", canonicalOutputID, "error", err)
			}
		}
	}
	if l.Deps.Streams != nil {
		cacheTokens := tokens
		if transientAssistant != nil || len(wireAssistant.ToolCalls) > 0 {
			cacheTokens = nil
		}
		l.Deps.Streams.CacheReplay(assistantMsg.ID, messageview.TranscriptMessage(wireAssistant).Content, cacheTokens)
	}
	promptAssistant := transientMessageFromStored(storedAssistant, transientAssistant)
	if transientAssistant != nil && st != nil {
		st.rememberSecretStorageOverlay(storedAssistant, promptAssistant)
	}
	return promptAssistant, nil
}

func (l *modelTurn) completeStream(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	profileID string,
	userPrompt string,
	iterIndex, maxIter int,
	hostTurn bool,
	st *promptLoopTurnState,
	onProgress func(*modelcall.Completion),
) (_ *modelcall.Completion, _ []string, err error) {
	// Secret overlays live for one outbound decision.
	if st.hasSecretStorageOverlays() {
		defer st.clearSecretStorageOverlays()
	}
	client := l.sessionRoutingClient(sess)
	isCoordinatorTurn := strings.TrimSpace(sess.ParentSessionID) == ""
	finishPreparing := func() {}
	if isCoordinatorTurn {
		activity := l.Projection.beginActivity(ctx, sess, sessionID, api.ActivityKindPreparingContext, "", "")
		finishPreparing = activity.finish
	}
	defer finishPreparing()
	llmCallID := uuid.NewString()
	preparationStarted := time.Now()
	turn, err := l.buildTurnRequest(ctx, sess, sessionID, history, profileID, userPrompt, iterIndex, maxIter, hostTurn, st)
	slog.InfoContext(ctx, "model request preparation", "call_id", llmCallID, "session_id", sessionID, "duration_ms", time.Since(preparationStarted).Milliseconds(), "failed", err != nil)
	if err != nil {
		return nil, nil, err
	}
	if st != nil {
		st.turnToolPlan = turn.ToolPlan
	}
	req, surfaceID, transcriptEstimate := turn.Req, turn.SurfaceID, turn.TranscriptEstimate
	// One id joins usage and completion records.
	req.Debug.CallID = llmCallID
	callProvider, callModel := l.resolveUsageMeta(sess, &modelcall.Completion{}) //nolint:contextcheck // pure registry lookup
	if ledger, ok := l.Deps.Cost.(cost.CallLedger); ok {
		if beginErr := ledger.BeginCall(ctx, cost.UsageEvent{
			ID: llmCallID, SessionID: sess.ID, ParentSessionID: strings.TrimSpace(sess.ParentSessionID),
			ProjectID: sess.ProjectID, ProviderID: callProvider, Model: callModel,
			Caller: promptLoopUsageCaller(sess), StartedAt: time.Now().UTC(),
		}); beginErr != nil {
			slog.WarnContext(ctx, "open llm call receipt", "call_id", llmCallID, "error", beginErr)
		} else {
			defer func() {
				if unknownErr := ledger.MarkCallUnknown(context.WithoutCancel(ctx), llmCallID); unknownErr != nil {
					slog.WarnContext(context.WithoutCancel(ctx), "mark interrupted llm call unknown", "call_id", llmCallID, "error", unknownErr)
				}
			}()
		}
	}
	proseFinish := st.proseTurn(sess, iterIndex, maxIter)
	ctx = llm.WithDispatchObserver(ctx, func() {
		finishPreparing()
		if l.Projection.Deps.Events == nil || !isCoordinatorTurn {
			return
		}
		loop := coordinatorLLMLoopProgress(profileID, surfaceID, l.Context.coordinatorSurfaceActivityLabel(surfaceID), hostTurn, iterIndex, maxIter, proseFinish)
		l.Projection.Deps.Events.PublishLLM(ctx, sessionProjectKey(sess), sessionID, api.LLMCallEvent{
			CallID:          llmCallID,
			Provider:        callProvider,
			Model:           callModel,
			Status:          api.LLMCallStatusActive,
			CoordinatorLoop: loop,
		})
	})
	if l.Projection.Deps.Events != nil && isCoordinatorTurn {
		// Publish one terminal event even after cancellation.
		defer func() {
			if err == nil {
				return
			}
			l.Projection.Deps.Events.PublishLLM(context.WithoutCancel(ctx), sessionProjectKey(sess), sessionID, api.LLMCallEvent{
				CallID:   llmCallID,
				Provider: callProvider,
				Model:    callModel,
				Status:   api.LLMCallStatusError,
				CoordinatorLoop: coordinatorLLMLoopProgress(
					profileID, surfaceID, l.Context.coordinatorSurfaceActivityLabel(surfaceID), hostTurn, iterIndex, maxIter, proseFinish,
				),
			})
		}()
	}

	// Retry updates reuse the call id.
	ctx = providerretry.WithRetryObserver(ctx, func(a providerretry.RetryAttempt) {
		if l.Projection.Deps.Events == nil || !isCoordinatorTurn {
			return
		}
		l.Projection.Deps.Events.PublishLLM(ctx, sessionProjectKey(sess), sessionID, api.LLMCallEvent{
			CallID:      llmCallID,
			Provider:    callProvider,
			Model:       callModel,
			Status:      api.LLMCallStatusActive,
			Attempt:     a.Attempt,
			MaxAttempts: a.MaxAttempts,
			RetryWaitMs: a.Wait.Milliseconds(),
			RetryReason: api.LLMRetryReason(a.Reason),
			SilenceMs:   a.Silence.Milliseconds(),
			CoordinatorLoop: coordinatorLLMLoopProgress(
				profileID, surfaceID, l.Context.coordinatorSurfaceActivityLabel(surfaceID), hostTurn, iterIndex, maxIter, proseFinish,
			),
		})
	})

	req, sourceRequest := modelcall.CaptureSourceRequest(req)
	collected, collectErr := l.collectPromptStream(
		ctx, sess, client, req, onProgress, llmCallID, callProvider, callModel)
	callProvider, callModel = collected.providerID, collected.model
	if collectErr != nil {
		err = collectErr
		return nil, nil, err
	}
	completion, tokens := collected.completion, collected.tokens
	providerID, model := collected.providerID, collected.model
	// What the provider delivered, before the tool-call contract rewrites it.
	delivered := completion

	completion, err = llm.EnforceToolCallSupport(l.providerProfile(providerID), req, completion, providerID, model)
	if err != nil {
		l.recordAbandonedTurnUsage(ctx, sess, llmCallID, providerID, model, req, delivered)
		return nil, nil, err
	}
	completion.SourceContext = sourceRequest.ForCompletion(completion)
	if st != nil {
		st.sourceContext = completion.SourceContext
	}

	if err := l.recordUsage(ctx, sess, llmCallID, providerID, model, req, completion); err != nil {
		slog.WarnContext(ctx, "record llm call usage", "call_id", llmCallID, "error", err)
	}
	outputGateStarted := time.Now()
	reject, blocked, transformed, changed := l.Closeout.evaluateContentAnchor(ctx, sess, oar.AnchorContentOutput, oarModelOutputSegments(completion.Content), "", nil)
	slog.InfoContext(ctx, "model output gate", "call_id", llmCallID, "session_id", sessionID, "duration_ms", time.Since(outputGateStarted).Milliseconds(), "blocked", blocked)
	if blocked {
		return nil, nil, reject
	} else if changed {
		completion.Content = transformed
		tokens = []string{transformed}
	}
	if l.Deps.RecordCompactionTokenObservation != nil && completion.Usage.PromptTokens > 0 {
		l.Deps.RecordCompactionTokenObservation(sessionID, completion.Usage.PromptTokens, transcriptEstimate)
	}
	if proseFinish {
		kept := retainOfferedToolCalls(nil, "", completion.ToolCalls, workerProseOfferedTools(sess))
		// Calls the final turn will not run are the model's miss, not a silent provider.
		if len(kept) == 0 && len(completion.ToolCalls) > 0 && strings.TrimSpace(completion.Content) == "" {
			return nil, nil, &ProseTurnToolCallError{ProviderID: providerID, Model: model, Tools: proseTurnToolNames(completion.ToolCalls), CloseoutReason: st.closeoutCauseTextOrEmpty()}
		}
		completion.ToolCalls = kept
	}
	// Empty completions fail before the terminal event.
	if !modelcall.CompletionHasPayload(completion) {
		return nil, nil, &failure.ProviderEmptyCompletionError{ProviderID: providerID, Model: model}
	}
	if isCoordinatorTurn {
		l.publishLLMCallOK(ctx, sess, sessionID, llmCallID, providerID, model, completion.Usage,
			coordinatorLLMLoopProgress(profileID, surfaceID, l.Context.coordinatorSurfaceActivityLabel(surfaceID), hostTurn, iterIndex, maxIter, proseFinish))
	}
	completion.ProviderID = providerID
	completion.Model = model
	return completion, tokens, nil
}

type promptStreamCollection struct {
	completion *modelcall.Completion
	tokens     []string
	providerID string
	model      string
}

func (l *modelTurn) collectPromptStream(
	ctx context.Context,
	sess *api.Session,
	client modelcall.LLMClient,
	req modelcall.CompletionRequest,
	onProgress func(*modelcall.Completion),
	callID, initialProvider, initialModel string,
) (promptStreamCollection, error) {
	result := promptStreamCollection{providerID: initialProvider, model: initialModel}
	streamCtx := ctx
	var stall time.Duration
	// The turn budget starts at send; pending approval time is excluded.
	var budget *llm.CallBudget
	if l.Context.Deps.Limits != nil {
		lim := l.Context.Deps.Limits(ctx, sess)
		budget = llm.NewCallBudget(lim.LLMTurnTimeout())
		streamCtx = llm.WithCallBudget(ctx, budget)
		stall = lim.LLMStreamStallTimeout()
	}
	streamCtx, cancelStall := context.WithCancelCause(streamCtx)
	defer cancelStall(nil)

	ch, err := client.Stream(streamCtx, req)
	if ctx.Err() != nil {
		err = context.Cause(ctx)
	}
	if err != nil {
		l.voidCostReceipt(context.WithoutCancel(ctx), callID)
		return result, err
	}
	ch = modelcall.GuardStreamStall(streamCtx, ch, stall, func() { cancelStall(modelcall.ErrStreamStall) })
	if onProgress != nil {
		result.completion, result.tokens, err = modelcall.CollectStreamWithProgress(ch, onProgress)
	} else {
		result.completion, result.tokens, err = modelcall.CollectStream(ch)
	}
	result.providerID, result.model = l.resolveUsageMeta(sess, result.completion) //nolint:contextcheck // pure lookup
	if ctx.Err() != nil {
		l.recordAbandonedTurnUsage(ctx, sess, callID, result.providerID, result.model, req, result.completion)
		return result, context.Cause(ctx)
	}
	// Stall and budget cancellation can return an error or end the stream.
	if timeout := turnTimeoutCause(streamCtx, budget); timeout != nil {
		l.recordAbandonedTurnUsage(ctx, sess, callID, result.providerID, result.model, req, result.completion)
		return result, fmt.Errorf("%w: %w", ErrLLMTurnTimeout, timeout)
	}
	if err != nil {
		l.recordAbandonedTurnUsage(ctx, sess, callID, result.providerID, result.model, req, result.completion)
		return result, err
	}
	return result, nil
}

// turnTimeoutCause reports why a turn ran out of time, or nil if it did not.
func turnTimeoutCause(streamCtx context.Context, budget *llm.CallBudget) error {
	if cause := context.Cause(streamCtx); errors.Is(cause, modelcall.ErrStreamStall) {
		return cause
	}
	if budget.Expired() {
		return llm.ErrCallBudgetExceeded
	}
	return nil
}

// coordinatorTurnRequest is one assembled provider call.
type coordinatorTurnRequest struct {
	Req modelcall.CompletionRequest
	// SurfaceID is empty outside coordinator profiles.
	SurfaceID string
	// TranscriptEstimate is the pre-call token estimate.
	TranscriptEstimate int
	ToolPlan           toolsurface.Plan
}

// buildTurnRequest assembles and detaches one provider request.
func (l *modelTurn) buildTurnRequest(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	profileID string,
	userPrompt string,
	iterIndex, maxIter int,
	hostTurn bool,
	st *promptLoopTurnState,
) (coordinatorTurnRequest, error) {
	if l.Context.Deps.BuildMessages == nil {
		return coordinatorTurnRequest{}, fmt.Errorf("build messages not configured")
	}
	frameValue := inject.CoordinatorTurnFrame{}
	var frame *inject.CoordinatorTurnFrame
	if st != nil && st.coordinatorFrameReady {
		frame = &st.coordinatorFrame
		frameValue = st.coordinatorFrame
	}
	messages, err := l.Context.Deps.BuildMessages(ctx, sess, history, frame)
	if err != nil {
		return coordinatorTurnRequest{}, err
	}
	if frame != nil {
		frameValue = *frame
	}
	if st != nil && st.proseTurn(sess, iterIndex, maxIter) {
		st.proseFinish = true
	}
	turnTools, toolPlan, surfaceID, err := l.Context.coordinatorToolsForTurn(
		ctx, sess, profileID, history, userPrompt, iterIndex, maxIter, frameValue, st,
	)
	if err != nil {
		return coordinatorTurnRequest{}, err
	}
	if st != nil && st.spendSoftStopGranted {
		turnTools = filterSpendSoftStopToolMetas(turnTools)
	}
	// A prose-only turn keeps its tool definitions, which transports validate
	// tool history against, and forbids calling them: a final turn with no
	// completion tool, or a report-document repair that asks for one fence.
	toolUse := modelcall.ToolUseAllowed
	callable := turnTools
	if l.Closeout.repairsReportDocument(ctx, sessionID) || (st != nil && st.proseFinish && len(workerProseOfferedTools(sess)) == 0) {
		toolUse = modelcall.ToolUseForbidden
		callable = []tools.ToolMeta{}
	}
	messages, err = l.Tools.appendToolProcedures(ctx, sess, profileID, messages, callable)
	if err != nil {
		return coordinatorTurnRequest{}, err
	}
	if reject, blocked, _, changed := l.Closeout.evaluateContentAnchor(ctx, sess, oar.AnchorContentInput, oarContentSegments(messages), "", nil); blocked {
		return coordinatorTurnRequest{}, reject
	} else if changed {
		return coordinatorTurnRequest{}, fmt.Errorf("[OAR-PROF-10] model.input transform cannot preserve message roles and provenance")
	}
	if l.Inbox.Deps.TakePolicyFeedback != nil {
		feedback, err := l.Inbox.Deps.TakePolicyFeedback(ctx, sessionID)
		if err != nil {
			return coordinatorTurnRequest{}, err
		}
		messages = append(messages, feedback...)
		if st != nil {
			st.history = append(st.history, feedback...)
		}
	}
	messages = l.fitMessagesForCompaction(ctx, sess, sessionID, messages, turnTools)
	// Detach the request from mutable history.
	messages = append([]api.Message(nil), messages...)
	req := modelcall.CompletionRequest{
		Messages: messages,
		Debug: modelcall.RequestDebug{
			SessionID:        sessionID,
			ProjectID:        sess.ProjectID,
			ProjectDir:       l.Context.primaryProjectRoot(ctx, sess),
			RootSessionID:    rootModelRequestSessionID(sess, sessionID),
			AgentType:        sess.AgentType,
			ParentSessionID:  sess.ParentSessionID,
			ProfileID:        profileID,
			HostTurn:         hostTurn,
			Surface:          surfaceID,
			Iteration:        iterIndex + 1,
			MaxIterations:    maxIter,
			WorkflowRevision: frameValue.WorkflowRevision,
		},
	}
	if err := validateTurnToolFunctionRoots(turnTools); err != nil {
		return coordinatorTurnRequest{}, err
	}
	req.Tools = turnTools
	req.ToolUse = toolUse
	st.stampOfferedToolNames(callable)
	return coordinatorTurnRequest{
		Req:                req,
		SurfaceID:          surfaceID,
		TranscriptEstimate: compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(messages)),
		ToolPlan:           toolPlan,
	}, nil
}

func validateTurnToolFunctionRoots(metas []tools.ToolMeta) error {
	for _, meta := range metas {
		if meta.ArgsSchema == nil {
			continue
		}
		if err := tools.ValidateFunctionParametersRoot(meta.ArgsSchema); err != nil {
			name := strings.TrimSpace(meta.Name)
			if name == "" {
				name = "unnamed"
			}
			return fmt.Errorf("tool %q: %w", name, err)
		}
	}
	return nil
}

// primaryProjectRoot keeps project leases stable across worker checkouts.
func (l *promptContext) primaryProjectRoot(ctx context.Context, sess *api.Session) string {
	if l == nil || sess == nil {
		return ""
	}
	return strings.TrimSpace(l.citationRoots(ctx, sess).ProjectDir)
}

func (l *modelTurn) fitMessagesForCompaction(ctx context.Context, sess *api.Session, sessionID string, messages []api.Message, turnTools []tools.ToolMeta) []api.Message {
	if l.Deps.CompactionConfig == nil {
		return messages
	}
	compCfg := l.Deps.CompactionConfig(ctx, sess)
	if !compCfg.Enabled || compCfg.HardCeilingTokens <= 0 {
		return messages
	}
	cal := compaction.PromptTokenCalibration{}
	if l.Deps.CompactionTokenCalibration != nil {
		cal = l.Deps.CompactionTokenCalibration(sessionID)
	}
	toolsJSON := ""
	if len(turnTools) > 0 {
		if raw, marshalErr := json.Marshal(turnTools); marshalErr == nil {
			toolsJSON = string(raw)
		}
	}
	maxTokens := compaction.FitMaxTokens(compCfg, cal, compCfg.ColdStartOverhead(compaction.ResolveColdStartOverhead(toolsJSON)))
	ctxMsgs := compaction.ContextMessagesFromAPI(messages)
	fitted := compaction.DeterministicFit(compCfg, ctxMsgs, maxTokens)
	return compaction.ContextMessagesToAPI(fitted, messages)
}
