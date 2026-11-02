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
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/internal/trainingcapture"
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
	modelIteration := nextModelIteration(st)
	messageID := uuid.NewString()
	if st != nil && coordinatorDraftSlotEligible(sess, profileID) {
		messageID = st.ensureCoordinatorDraftSlot()
	}
	outputID := uuid.NewString()
	ctx, capture := trainingcapture.Begin(ctx, sessionID, messageID, outputID)
	defer capture.Flush(ctx)
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
		return api.Message{}, completion, nil, withdrawFailedDraft(err)
	}
	if err := persistPlaceholder(); err != nil {
		return api.Message{}, nil, nil, err
	}
	clearStream()
	promptAssistant, err := l.Projection.settleAssistantStreamTurn(
		ctx, sessionID, st, assistantMsg, completion, tokens, outputID, modelIteration, wireContent,
	)
	if err != nil {
		return api.Message{}, completion, nil, withdrawFailedDraft(err)
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
	iteration int,
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
			Iteration: iteration, MessageID: assistantMsg.ID,
			ProviderID: completion.ProviderID, Model: completion.Model, Scripted: completion.Scripted,
			Content: wireAssistant.Content, ToolCallsJSON: string(callsJSON),
			ReasoningJSON: string(reasoningJSON), CreatedAt: assistantMsg.CreatedAt,
		})
		if settleErr != nil {
			return api.Message{}, settleErr
		}
		st.output.id = committed.ID
		canonicalOutputID = committed.ID
		trainingcapture.Settled(ctx, committed.ID, wireAssistant)
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

func (l *turnProjection) seedModelIteration(ctx context.Context, attemptID string, st *promptLoopTurnState) error {
	if st == nil || strings.TrimSpace(attemptID) == "" || l == nil || l.Deps.SettledModelOutputIteration == nil {
		return nil
	}
	settledIteration, err := l.Deps.SettledModelOutputIteration(ctx, attemptID)
	if err != nil {
		return err
	}
	st.output.iteration = settledIteration
	return nil
}

// nextModelIteration allocates the next 1-based model-output iteration for the attempt.
func nextModelIteration(st *promptLoopTurnState) int {
	if st == nil {
		return 1
	}
	st.output.iteration++
	return st.output.iteration
}

func currentModelIteration(st *promptLoopTurnState, iterIndex int) int {
	if st != nil && st.output.iteration > 0 {
		return st.output.iteration
	}
	return iterIndex + 1
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
	return l.completeStreamWithOverflowRetry(
		ctx, sess, sessionID, history, profileID, userPrompt, iterIndex, maxIter, hostTurn, st, onProgress, false,
	)
}

func (l *modelTurn) completeStreamWithOverflowRetry(
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
	overflowRecovered bool,
) (_ *modelcall.Completion, _ []string, err error) {
	// Secret overlays live for one outbound decision.
	if st.hasSecretStorageOverlays() {
		defer st.clearSecretStorageOverlays()
	}
	client := l.sessionRoutingClient(sess)
	isCoordinatorTurn := strings.TrimSpace(sess.ParentSessionID) == ""
	finishPreparing := func() {}
	if isCoordinatorTurn {
		finishPreparing = l.Projection.beginActivity(ctx, sess, sessionID, api.ActivityKindPreparingContext, "", "").finish
	}
	defer finishPreparing()
	llmCallID := uuid.NewString()
	preparationStarted := time.Now()
	turn, err := l.buildTurnRequest(ctx, sess, sessionID, history, profileID, userPrompt, iterIndex, maxIter, hostTurn, st)
	slog.InfoContext(ctx, "model request preparation", "call_id", llmCallID, "session_id", sessionID, "duration_ms", time.Since(preparationStarted).Milliseconds(), "failed", err != nil)
	if err != nil {
		if !overflowRecovered {
			if tooSmall, ok := failure.AsProviderContextTooSmall(err); ok && tooSmall != nil {
				finishPreparing()
				history = l.recoverFromContextOverflow(ctx, sess, sessionID, history, st, "", tooSmall.ProviderID, tooSmall.Model, 0, tooSmall)
				return l.completeStreamWithOverflowRetry(
					ctx, sess, sessionID, history, profileID, userPrompt, iterIndex, maxIter, hostTurn, st, onProgress, true,
				)
			}
		}
		return nil, nil, err
	}
	if st != nil {
		st.turnToolPlan = turn.ToolPlan
	}
	req, surfaceID, transcriptEstimate := turn.Req, turn.SurfaceID, turn.TranscriptEstimate
	// One id joins usage and completion records.
	req.Debug.CallID = llmCallID
	callProvider, callModel := l.resolveUsageMeta(sess, &modelcall.Completion{}) //nolint:contextcheck // pure registry lookup
	defer beginTurnCallLedger(ctx, l.Deps.Cost, sess, llmCallID, callProvider, callModel)()
	proseFinish := st.proseTurn(sess, iterIndex, maxIter)
	progressIterIndex := currentModelIteration(st, iterIndex) - 1
	loopProgress := func() *api.CoordinatorLoopProgress {
		return coordinatorLLMLoopProgress(profileID, surfaceID, l.Context.coordinatorSurfaceActivityLabel(surfaceID), hostTurn, progressIterIndex, maxIter, proseFinish)
	}
	ctx = attachStreamObservers(ctx, l, sess, sessionID, llmCallID, callProvider, callModel, isCoordinatorTurn, finishPreparing, loopProgress)
	callFailed := func() {
		if isCoordinatorTurn {
			l.publishLLMCallError(ctx, sess, sessionID, llmCallID, callProvider, callModel, loopProgress())
		}
	}
	suppressCallFailed := false
	// Publish one terminal event even after cancellation.
	defer func() {
		if err != nil && !suppressCallFailed {
			callFailed()
		}
	}()

	req.EmptyResponseRecovery = l.emptyResponseRecovery(sess, req)
	req = trainingcapture.Prepare(ctx, req, l.trainingCapturePolicy(ctx))
	req, sourceRequest := modelcall.CaptureSourceRequest(req)
	if st != nil {
		st.output.recoveryRejected = false
	}
	collected, collectErr := l.collectPromptStream(ctx, sess, client, req, onProgress, llmCallID, callProvider, callModel)
	callProvider, callModel = collected.providerID, collected.model
	trainingcapture.Observe(ctx, collected.completion, collectErr != nil, callProvider, callModel)
	if collectErr != nil {
		if !overflowRecovered {
			if tooSmall, ok := failure.AsProviderContextTooSmall(collectErr); ok && tooSmall != nil {
				suppressCallFailed = true
				history = l.recoverFromContextOverflow(ctx, sess, sessionID, history, st, surfaceID, callProvider, callModel, transcriptEstimate, tooSmall)
				return l.completeStreamWithOverflowRetry(
					ctx, sess, sessionID, history, profileID, userPrompt, iterIndex, maxIter, hostTurn, st, onProgress, true,
				)
			}
		}
		recovered, rerr := handleStreamCollectError(ctx, l, req, collected, collectErr, func() (*modelcall.Completion, error) {
			return l.recoverRejectedResponse(ctx, sess, st, req, sourceRequest, collected.completion, collectErr, maxIter-iterIndex-1)
		}, callFailed)
		return recovered, nil, rerr
	}
	completion, tokens := collected.completion, collected.tokens
	providerID, model := collected.providerID, collected.model
	// What the provider delivered, before the tool-call contract rewrites it.
	delivered := completion
	if truncErr := truncatedCompletionFault(ctx, l, sess, llmCallID, req, providerID, model, completion); truncErr != nil {
		return completion, nil, truncErr
	}

	response, err := llm.ClassifyToolResponse(l.providerProfile(providerID), req, completion, providerID, model)
	if err != nil {
		l.recordAbandonedTurnUsage(ctx, sess, llmCallID, providerID, model, req, delivered)
		return nil, nil, err
	}
	completion = response.Completion
	completion.SourceContext = sourceRequest.ForCompletion(completion)
	if st != nil {
		st.output.sourceContext = completion.SourceContext
	}

	if err := l.recordUsage(ctx, sess, llmCallID, providerID, model, req, completion); err != nil {
		slog.WarnContext(ctx, "record llm call usage", "call_id", llmCallID, "error", err)
	}
	outputGateStarted := time.Now()
	reject, blocked, transformed, changed := l.Closeout.evaluateContentAnchor(ctx, sess, oar.AnchorContentOutput, oar.ContentObservation{Segments: oarModelOutputSegments(completion.Content), TextActionTools: response.TextActionTools}, "", nil)
	slog.InfoContext(ctx, "model output gate", "call_id", llmCallID, "session_id", sessionID, "duration_ms", time.Since(outputGateStarted).Milliseconds(), "blocked", blocked, "selected_text_protocol", req.ToolProtocol, "text_action_tools", response.TextActionTools)
	if st != nil {
		st.output.rejection = nil
		if !blocked {
			st.output.recovery.Accepted()
		}
	}
	if blocked {
		if st == nil || len(response.TextActionTools) == 0 || reject == nil || reject.Code() != "TOOL_ACTION_IN_TEXT" {
			return nil, nil, reject
		}
		st.output.rejection = reject
	} else if changed {
		completion.Content = transformed
		tokens = []string{transformed}
	}
	if input := completion.ContextInputTokens(); l.Deps.RecordCompactionTokenObservation != nil && input > 0 {
		l.Deps.RecordCompactionTokenObservation(sessionID, callProvider, callModel, input, transcriptEstimate)
	}
	if proseFinish {
		kept := retainOfferedToolCalls(nil, "", completion.ToolCalls, workerProseOfferedTools(sess))
		if len(kept) == 0 && len(completion.ToolCalls) > 0 && strings.TrimSpace(completion.Content) == "" && allBookkeepingToolCalls(completion.ToolCalls) {
			if retryComp, retryTokens, ok := l.retryProseFinishAfterBookkeeping(ctx, sess, client, req, onProgress, llmCallID, providerID, model); ok {
				retryComp.SourceContext = sourceRequest.ForCompletion(retryComp)
				if st != nil {
					st.output.sourceContext = retryComp.SourceContext
				}
				completion = retryComp
				tokens = retryTokens
			}
		}
		// Calls the final turn will not run are the model's miss, not a silent provider.
		if len(kept) == 0 && len(completion.ToolCalls) > 0 && strings.TrimSpace(completion.Content) == "" {
			return nil, nil, &ProseTurnToolCallError{ProviderID: providerID, Model: model, Tools: proseTurnToolNames(completion.ToolCalls), CloseoutReason: st.closeoutCauseTextOrEmpty()}
		}
		completion.ToolCalls = kept
	}
	// Empty completions fail before the terminal event.
	if !modelcall.CompletionHasPayload(completion) {
		return completion, nil, emptyOrTruncatedCompletionError(ctx, l, req, providerID, model, completion)
	}
	if isCoordinatorTurn {
		l.publishLLMCallOK(ctx, sess, sessionID, llmCallID, providerID, model, completion.Usage,
			coordinatorLLMLoopProgress(profileID, surfaceID, l.Context.coordinatorSurfaceActivityLabel(surfaceID), hostTurn, progressIterIndex, maxIter, proseFinish))
	}
	completion.ProviderID = providerID
	completion.Model = model
	return completion, tokens, nil
}

func beginTurnCallLedger(ctx context.Context, costDep any, sess *api.Session, llmCallID, callProvider, callModel string) func() {
	ledger, ok := costDep.(cost.CallLedger)
	if !ok {
		return func() {}
	}
	if beginErr := ledger.BeginCall(ctx, cost.UsageEvent{
		ID: llmCallID, SessionID: sess.ID, ParentSessionID: strings.TrimSpace(sess.ParentSessionID),
		ProjectID: sess.ProjectID, ProviderID: callProvider, Model: callModel,
		Caller: promptLoopUsageCaller(sess), StartedAt: time.Now().UTC(),
	}); beginErr != nil {
		slog.WarnContext(ctx, "open llm call receipt", "call_id", llmCallID, "error", beginErr)
		return func() {}
	}
	return func() {
		if unknownErr := ledger.MarkCallUnknown(context.WithoutCancel(ctx), llmCallID); unknownErr != nil {
			slog.WarnContext(context.WithoutCancel(ctx), "mark interrupted llm call unknown", "call_id", llmCallID, "error", unknownErr)
		}
	}
}

func attachStreamObservers(
	ctx context.Context,
	l *modelTurn,
	sess *api.Session,
	sessionID, llmCallID, callProvider, callModel string,
	isCoordinatorTurn bool,
	finishPreparing func(),
	loopProgress func() *api.CoordinatorLoopProgress,
) context.Context {
	ctx = llm.WithDispatchObserver(ctx, func() {
		finishPreparing()
		if l.Projection.Deps.Events == nil || !isCoordinatorTurn {
			return
		}
		l.Projection.Deps.Events.PublishLLM(ctx, sessionProjectKey(sess), sessionID, api.LLMCallEvent{
			CallID:          llmCallID,
			Provider:        callProvider,
			Model:           callModel,
			Status:          api.LLMCallStatusActive,
			CoordinatorLoop: loopProgress(),
		})
	})
	return providerretry.WithRetryObserver(ctx, func(a providerretry.RetryAttempt) {
		if l.Projection.Deps.Events == nil || !isCoordinatorTurn {
			return
		}
		l.Projection.Deps.Events.PublishLLM(ctx, sessionProjectKey(sess), sessionID, api.LLMCallEvent{
			CallID:          llmCallID,
			Provider:        callProvider,
			Model:           callModel,
			Status:          api.LLMCallStatusActive,
			Attempt:         a.Attempt,
			MaxAttempts:     a.MaxAttempts,
			RetryWaitMs:     a.Wait.Milliseconds(),
			RetryReason:     api.LLMRetryReason(a.Reason),
			SilenceMs:       a.Silence.Milliseconds(),
			CoordinatorLoop: loopProgress(),
		})
	})
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
