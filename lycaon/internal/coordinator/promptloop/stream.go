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
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
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

func (l *PromptLoop) runAssistantStreamTurn(
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
	if st != nil && st.attemptID != "" && l.Deps.AdmitModelResponse != nil {
		if err := l.Deps.AdmitModelResponse(ctx, sessionID, st.attemptID); err != nil {
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
	if l.Deps.AppendMessages == nil {
		return api.Message{}, nil, nil, fmt.Errorf("append messages not configured")
	}
	var clearActive sync.Once
	clearStream := func() {
		clearActive.Do(func() {
			if l.Deps.Streams != nil {
				l.Deps.Streams.Finish(ctx, sessionID)
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
		withdrawErr := l.withdrawCoordinatorDraft(
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
		if err := l.Deps.AppendMessages(ctx, sessionID, assistantMsg); err != nil {
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
	wireContent := closeoutWireContent(l.promptTurnSurface(sessionID))
	completion, tokens, err := l.completeStream(ctx, sess, sessionID, history, profileID, userPrompt, iterIndex, maxIter, hostTurn, st, func(partial *modelcall.Completion) {
		if partial == nil {
			return
		}
		// [OAR-OPS-8] Content and tool-call deltas remain private until model.output resolves.
		if l.Deps.Streams != nil {
			generating := tokenest.EstimateDefault(partial.Content) + tokenest.EstimateDefault(partial.Reasoning)
			l.Deps.Streams.CacheLive(sessionID, assistantMsg.ID, "", nil, generating)
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
	promptAssistant, err := l.settleAssistantStreamTurn(
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

func (l *PromptLoop) settleAssistantStreamTurn(
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
func (l *PromptLoop) buildTurnRequest(
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
	if l.Deps.BuildMessages == nil {
		return coordinatorTurnRequest{}, fmt.Errorf("build messages not configured")
	}
	frameValue := inject.CoordinatorTurnFrame{}
	var frame *inject.CoordinatorTurnFrame
	if st != nil && st.coordinatorFrameReady {
		frame = &st.coordinatorFrame
		frameValue = st.coordinatorFrame
	}
	messages, err := l.Deps.BuildMessages(ctx, sess, history, frame)
	if err != nil {
		return coordinatorTurnRequest{}, err
	}
	if frame != nil {
		frameValue = *frame
	}
	if st != nil && st.proseTurn(sess, iterIndex, maxIter) {
		st.proseFinish = true
	}
	turnTools, toolPlan, surfaceID, err := l.coordinatorToolsForTurn(
		ctx, sess, profileID, history, userPrompt, iterIndex, maxIter, frameValue, st,
	)
	if err != nil {
		return coordinatorTurnRequest{}, err
	}
	if st != nil && st.spendSoftStopGranted {
		turnTools = filterSpendSoftStopToolMetas(turnTools)
	}
	// A report-document repair asks for one fence; an offered tool only
	// invites a call that is not that fence.
	if l.repairsReportDocument(ctx, sessionID) {
		turnTools = nil
	}
	messages, err = l.appendToolProcedures(ctx, sess, profileID, messages, turnTools)
	if err != nil {
		return coordinatorTurnRequest{}, err
	}
	if reject, blocked, _, changed := l.evaluateContentAnchor(ctx, sess, oar.AnchorContentInput, oarContentSegments(messages), "", nil); blocked {
		return coordinatorTurnRequest{}, reject
	} else if changed {
		return coordinatorTurnRequest{}, fmt.Errorf("[OAR-PROF-10] model.input transform cannot preserve message roles and provenance")
	}
	if l.Deps.TakePolicyFeedback != nil {
		feedback, err := l.Deps.TakePolicyFeedback(ctx, sessionID)
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
			ProjectDir:       l.primaryProjectRoot(ctx, sess),
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
	st.stampOfferedToolNames(turnTools)
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
func (l *PromptLoop) primaryProjectRoot(ctx context.Context, sess *api.Session) string {
	if l == nil || sess == nil {
		return ""
	}
	return strings.TrimSpace(l.citationRoots(ctx, sess).ProjectDir)
}

func (l *PromptLoop) completeStream(
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
		activity := l.beginActivity(ctx, sess, sessionID, api.ActivityKindPreparingContext, "", "")
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
		if l.Deps.Events == nil || !isCoordinatorTurn {
			return
		}
		loop := coordinatorLLMLoopProgress(profileID, surfaceID, l.coordinatorSurfaceActivityLabel(surfaceID), hostTurn, iterIndex, maxIter, proseFinish)
		l.Deps.Events.PublishLLM(ctx, sessionProjectKey(sess), sessionID, api.LLMCallEvent{
			CallID:          llmCallID,
			Provider:        callProvider,
			Model:           callModel,
			Status:          api.LLMCallStatusActive,
			CoordinatorLoop: loop,
		})
	})
	if l.Deps.Events != nil && isCoordinatorTurn {
		// Publish one terminal event even after cancellation.
		defer func() {
			if err == nil {
				return
			}
			l.Deps.Events.PublishLLM(context.WithoutCancel(ctx), sessionProjectKey(sess), sessionID, api.LLMCallEvent{
				CallID:   llmCallID,
				Provider: callProvider,
				Model:    callModel,
				Status:   api.LLMCallStatusError,
				CoordinatorLoop: coordinatorLLMLoopProgress(
					profileID, surfaceID, l.coordinatorSurfaceActivityLabel(surfaceID), hostTurn, iterIndex, maxIter, proseFinish,
				),
			})
		}()
	}

	// Retry updates reuse the call id.
	ctx = providerretry.WithRetryObserver(ctx, func(a providerretry.RetryAttempt) {
		if l.Deps.Events == nil || !isCoordinatorTurn {
			return
		}
		l.Deps.Events.PublishLLM(ctx, sessionProjectKey(sess), sessionID, api.LLMCallEvent{
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
				profileID, surfaceID, l.coordinatorSurfaceActivityLabel(surfaceID), hostTurn, iterIndex, maxIter, proseFinish,
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
	reject, blocked, transformed, changed := l.evaluateContentAnchor(ctx, sess, oar.AnchorContentOutput, oarModelOutputSegments(completion.Content), "", nil)
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
		completion.ToolCalls = retainOfferedToolCalls(nil, "", completion.ToolCalls, workerProseOfferedTools(sess))
	}
	// Empty completions fail before the terminal event.
	if !modelcall.CompletionHasPayload(completion) {
		return nil, nil, &failure.ProviderEmptyCompletionError{ProviderID: providerID, Model: model}
	}
	if isCoordinatorTurn {
		l.publishLLMCallOK(ctx, sess, sessionID, llmCallID, providerID, model, completion.Usage,
			coordinatorLLMLoopProgress(profileID, surfaceID, l.coordinatorSurfaceActivityLabel(surfaceID), hostTurn, iterIndex, maxIter, proseFinish))
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

func (l *PromptLoop) collectPromptStream(
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
	if l.Deps.Limits != nil {
		lim := l.Deps.Limits(ctx, sess)
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

func (l *PromptLoop) voidCostReceipt(ctx context.Context, callID string) {
	ledger, ok := l.Deps.Cost.(cost.CallLedger)
	if !ok || strings.TrimSpace(callID) == "" {
		return
	}
	if err := ledger.VoidCall(ctx, callID); err != nil {
		slog.WarnContext(ctx, "void llm call receipt", "call_id", callID, "error", err)
	}
}

// publishLLMCallOK reports a completed coordinator call.
func (l *PromptLoop) publishLLMCallOK(
	ctx context.Context,
	sess *api.Session,
	sessionID, callID, providerID, model string,
	usage modelcall.TokenUsage,
	loop *api.CoordinatorLoopProgress,
) {
	if l.Deps.Events == nil {
		return
	}
	var contextWindow, compactionThreshold int
	if l.Deps.CompactionConfig != nil {
		cc := l.Deps.CompactionConfig(ctx, sess)
		contextWindow = cc.ModelContextWindow
		compactionThreshold = cc.CompactionTriggerTokens()
	}
	l.Deps.Events.PublishLLM(ctx, sessionProjectKey(sess), sessionID, api.LLMCallEvent{
		CallID:   callID,
		Provider: providerID,
		Model:    model,
		Status:   api.LLMCallStatusOK,
		Tokens: api.LLMTokenCounts{
			Prompt:              usage.PromptTokens,
			Completion:          usage.CompletionTokens,
			Total:               usage.PromptTokens + usage.CompletionTokens,
			ContextWindow:       contextWindow,
			CompactionThreshold: compactionThreshold,
		},
		CoordinatorLoop: loop,
	})
}

func rootModelRequestSessionID(sess *api.Session, sessionID string) string {
	if sess != nil && strings.TrimSpace(sess.ParentSessionID) != "" {
		return strings.TrimSpace(sess.ParentSessionID)
	}
	return strings.TrimSpace(sessionID)
}

func (l *PromptLoop) fitMessagesForCompaction(ctx context.Context, sess *api.Session, sessionID string, messages []api.Message, turnTools []tools.ToolMeta) []api.Message {
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

func (l *PromptLoop) sessionRoutingClient(sess *api.Session) modelcall.LLMClient {
	client := llm.WithClientDispatch(l.Deps.LLM)
	if l.Deps.LLMService != nil && l.Deps.LLMService.Registry != nil && l.Deps.LLMService.Router != nil {
		client = llm.NewRoutingClient(l.Deps.LLMService.Registry, l.Deps.LLMService.Router, l.Deps.LLM, l.Deps.LLMService.Utility, l.Deps.LLMService.Capacity, l.Deps.LLMService.Refusals, func(ctx context.Context) (*llm.ModelSelection, error) {
			router := l.Deps.LLMService.Router.WithOverlayRoots(l.overlayRootPaths(ctx, sess))
			return router.ResolveSession(ctx, sess)
		})
	}
	if l.Deps.LLMService != nil && l.Deps.LLMService.Preparation != nil {
		client = l.Deps.LLMService.Preparation.Wrap(client)
	}
	return client
}

// providerProfile uses the default driver profile for unknown provider IDs.
func (l *PromptLoop) providerProfile(providerID string) providerprofile.Profile {
	if l.Deps.LLMService != nil && l.Deps.LLMService.Registry != nil && strings.TrimSpace(providerID) != "" {
		if p, err := l.Deps.LLMService.Registry.Get(providerID); err == nil && p != nil {
			return p.Profile()
		}
	}
	return providerprofile.Default()
}

func (l *PromptLoop) resolveUsageMeta(sess *api.Session, completion *modelcall.Completion) (providerID, model string) {
	if completion != nil && completion.ProviderID != "" {
		return completion.ProviderID, completion.Model
	}
	if l.Deps.LLMService != nil && l.Deps.LLMService.Registry != nil && l.Deps.LLMService.Router != nil {
		ctx := context.Background()
		router := l.Deps.LLMService.Router.WithOverlayRoots(l.overlayRootPaths(ctx, sess))
		sel, err := router.ResolveSession(ctx, sess)
		if err == nil && sel != nil {
			return sel.ProviderID, sel.Model
		}
	}
	return "", ""
}

// recordUsage records reported or host-measured usage.
func (l *PromptLoop) recordUsage(
	ctx context.Context, sess *api.Session, callID, providerID, model string,
	req modelcall.CompletionRequest, completion *modelcall.Completion,
) error {
	if l.Deps.Cost == nil {
		return nil
	}
	usage, source := modelcall.TokenUsage{}, cost.UsageFromProvider
	switch {
	case completion != nil && completion.Usage.Reported():
		usage = completion.Usage
	case modelcall.CompletionHasPayload(completion):
		usage, source = llm.MeasureUsage(req, completion), cost.UsageFromHost
	default:
		if ledger, ok := l.Deps.Cost.(cost.CallLedger); ok {
			return ledger.MarkCallUnknown(ctx, callID)
		}
		return nil
	}
	sessionID := sess.ID
	est, err := l.Deps.Cost.Estimate(ctx, providerID, model, cost.TokenUsage{
		PromptTokens:               usage.PromptTokens,
		CompletionTokens:           usage.CompletionTokens,
		CacheReadInputTokens:       usage.CacheReadInputTokens,
		CacheCreationInputTokens:   usage.CacheCreationInputTokens,
		CacheCreation1HInputTokens: usage.CacheCreation1HInputTokens,
	})
	if err != nil {
		slog.WarnContext(ctx, "estimate llm call cost", "call_id", callID, "error", err)
		est = cost.CostEstimate{Currency: "USD", Unpriced: true}
	}
	if usage.Incomplete {
		source = cost.UsageFromProviderPartial
	}
	estimated, cacheSavings, err := est.NanoUSD()
	if err != nil {
		return err
	}
	if err := l.Deps.Cost.RecordUsage(ctx, cost.UsageEvent{
		ID:                         callID,
		SessionID:                  sessionID,
		ParentSessionID:            strings.TrimSpace(sess.ParentSessionID),
		ProjectID:                  sess.ProjectID,
		ProviderID:                 providerID,
		Model:                      model,
		PromptTokens:               usage.PromptTokens,
		CompletionTokens:           usage.CompletionTokens,
		CacheReadInputTokens:       usage.CacheReadInputTokens,
		CacheCreationInputTokens:   usage.CacheCreationInputTokens,
		CacheCreation1HInputTokens: usage.CacheCreation1HInputTokens,
		EstimatedNanoUSD:           estimated,
		Unpriced:                   est.Unpriced,
		UnpricedTokens:             est.UnpricedTokens,
		RateSnapshot:               est.RateSnapshot,
		CacheSavingsNanoUSD:        cacheSavings,
		UnpricedCacheTokens:        est.UnpricedCacheTokens,
		PricingSource:              est.PricingSource,
		PricedAsOf:                 est.PricedAsOf,
		Caller:                     promptLoopUsageCaller(sess),
		UsageSource:                source,
	}); err != nil {
		return err
	}
	if l.Deps.Events != nil {
		// Worker usage updates the root session rollup.
		scopeID := l.costRollupScopeID(ctx, sess)
		if summary, err := l.Deps.Cost.Summary(ctx, api.CostScopeSession, scopeID, ""); err == nil {
			l.Deps.Events.PublishCost(ctx, sessionProjectKey(sess), scopeID, cost.CostEventFromSummary(summary))
		}
	}
	return nil
}

// costRollupScopeID returns the top-level session.
func (l *PromptLoop) costRollupScopeID(ctx context.Context, sess *api.Session) string {
	if strings.TrimSpace(sess.ParentSessionID) == "" {
		return sess.ID
	}
	if l.Deps.RootSessionID != nil {
		if root := strings.TrimSpace(l.Deps.RootSessionID(ctx, sess.ID)); root != "" {
			return root
		}
	}
	return strings.TrimSpace(sess.ParentSessionID)
}

// recordAbandonedTurnUsage preserves observable usage after failure.
func (l *PromptLoop) recordAbandonedTurnUsage(
	ctx context.Context, sess *api.Session, callID, providerID, model string,
	req modelcall.CompletionRequest, completion *modelcall.Completion,
) {
	ctx = context.WithoutCancel(ctx)
	if err := l.recordUsage(ctx, sess, callID, providerID, model, req, completion); err != nil {
		slog.WarnContext(ctx, "cost usage for abandoned turn not recorded",
			"session_id", sess.ID, "provider", providerID, "model", model, "error", err)
	}
}

func promptLoopUsageCaller(sess *api.Session) string {
	if strings.TrimSpace(sess.ParentSessionID) != "" {
		return cost.CallerWorker
	}
	return cost.CallerCoordinator
}

func sessionProjectKey(sess *api.Session) string {
	if sess == nil {
		return ""
	}
	return strings.TrimSpace(sess.ProjectID)
}

func coordinatorLLMLoopProgress(profileID, surfaceID, activityLabel string, hostTurn bool, iterIndex, maxIter int, proseFinish bool) *api.CoordinatorLoopProgress {
	if !guard.IsCoordinatorProfile(profileID) {
		return nil
	}
	loop := &api.CoordinatorLoopProgress{
		Surface:           surfaceID,
		ActivityLabel:     strings.TrimSpace(activityLabel),
		Guarded:           true,
		ProvisionalHidden: surface.SurfaceDeliversReport(surfaceID),
		Iteration:         iterIndex + 1,
		MaxIterations:     maxIter,
		ProseFinish:       proseFinish,
	}
	if hostTurn {
		loop.HostTurn = true
	}
	return loop
}

func (l *PromptLoop) coordinatorSurfaceActivityLabel(surfaceID string) string {
	if l == nil || l.Deps.CoordinatorSurfaceActivityLabel == nil {
		return ""
	}
	return l.Deps.CoordinatorSurfaceActivityLabel(surfaceID)
}

func (l *PromptLoop) overlayRootPaths(ctx context.Context, sess *api.Session) []string {
	if l == nil || l.Deps.OverlayRootPaths == nil || sess == nil {
		return nil
	}
	return l.Deps.OverlayRootPaths(ctx, sess)
}

func (l *PromptLoop) evaluateContentAnchor(ctx context.Context, sess *api.Session, anchor string, segments []oar.ContentSegment, tool string, args map[string]any) (*guidance.Refusal, bool, string, bool) {
	if l == nil || l.Deps.EvaluateContentAnchor == nil {
		return nil, false, "", false
	}
	return l.Deps.EvaluateContentAnchor(ctx, sess, anchor, segments, tool, args)
}
