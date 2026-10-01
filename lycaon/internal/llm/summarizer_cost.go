package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

// CallLifecycle publishes a session-scoped provider-call edge.
type CallLifecycle interface {
	PublishCall(ctx context.Context, ev api.LLMCallEvent)
}

// utilityAttempt tracks one budgeted provider call and receipt.
type utilityAttempt struct {
	summarizer *RegistrySummarizer
	provider   string
	model      string
	callID     string
	// ctx carries the budget this call spends once the request leaves.
	ctx    context.Context
	budget *CallBudget
	// books lets receipt writes survive cancellation.
	books   context.Context
	settled bool
}

// beginUtilityCall budgets a call and opens its receipt.
func (r *RegistrySummarizer) beginUtilityCall(ctx context.Context, p modelcall.Provider, model string) *utilityAttempt {
	budget := NewCallBudget(resolveUtilityAttemptTimeout(p, r.utilityClass()))
	attempt := &utilityAttempt{
		summarizer: r, model: model, ctx: WithCallBudget(ctx, budget), budget: budget,
		books: context.WithoutCancel(ctx),
	}
	if p != nil {
		attempt.provider = p.ID()
	}
	if ctx.Err() != nil {
		attempt.settled = true
		return attempt
	}
	attempt.callID = r.openSummarizerReceipt(attempt.books, attempt.provider, model) //nolint:contextcheck // books is ctx with cancellation stripped
	return attempt
}

// utilityRequest stamps the same ownership and composition on streaming and blocking calls.
func (r *RegistrySummarizer) utilityRequest(ctx context.Context, req modelcall.CompletionRequest) modelcall.CompletionRequest {
	req.Think = modelcall.ThinkOff
	req.Composition = modelcall.CompositionHostUtility
	if strings.TrimSpace(req.Debug.Purpose) == "" && r != nil {
		req.Debug.Purpose = r.Purpose
	}
	if r != nil {
		req.Debug.ProjectID = firstNonEmpty(req.Debug.ProjectID, r.ProjectID)
		req.Debug.ProjectDir = firstNonEmpty(req.Debug.ProjectDir, r.ProjectDir)
	}
	req.Debug = stampSessionAttribution(req.Debug, curationctx.SessionFrom(ctx))
	return req
}

// completeUtility stamps attribution and records one budgeted utility call.
func (r *RegistrySummarizer) completeUtility(ctx context.Context, p modelcall.Provider, model string, req modelcall.CompletionRequest) (resp *modelcall.Completion, err error) {
	req = r.utilityRequest(ctx, req)
	class := r.utilityClass()
	observe := r.observingLiteSlot(p)
	if observe && r != nil && r.Plane != nil && !r.Plane.allowLite() {
		return nil, ErrLiteUnavailable
	}
	release, laneErr := r.planeAcquire(ctx, p, class)
	if laneErr != nil {
		return nil, laneErr
	}
	defer release()
	attempt := r.beginUtilityCall(ctx, p, model)
	defer attempt.close()
	callID, occupy := r.beginLaneCall(ctx, attempt.callID, attempt.provider, model)
	defer r.endLaneCall(ctx, occupy, callID, attempt.provider, model, &err)
	resp, err = p.Complete(attempt.ctx, req) //nolint:contextcheck // attempt.ctx is ctx plus this call's budget
	if err != nil && attempt.budget.Expired() && ctx.Err() == nil {
		err = &utilityBudgetExceededError{cause: err}
	}
	if err != nil && req.ResponseFormat != nil && providerretry.IsRequestRejected(err) {
		// Unsupported formats fail before generation.
		attempt.void()
		return nil, err
	}
	r.noteCallOutcome(observe, providerIDOf(p), model, err)
	attempt.finish(req, resp)
	if err == nil && resp == nil {
		return nil, fmt.Errorf("provider %s returned no completion", attempt.provider)
	}
	return resp, err
}

func (r *RegistrySummarizer) occupyLane(ctx context.Context) bool {
	if r == nil || r.Lifecycle == nil {
		return false
	}
	if !curationctx.LaneOccupied(ctx) {
		return false
	}
	return strings.TrimSpace(curationctx.SessionFrom(ctx).SessionID) != ""
}

func (r *RegistrySummarizer) publishUtilityCall(ctx context.Context, ev api.LLMCallEvent) {
	if !r.occupyLane(ctx) {
		return
	}
	r.Lifecycle.PublishCall(ctx, ev)
}

func (r *RegistrySummarizer) beginLaneCall(ctx context.Context, callID, provider, model string) (string, bool) {
	if !r.occupyLane(ctx) {
		return "", false
	}
	if strings.TrimSpace(callID) == "" {
		callID = uuid.NewString()
	}
	r.publishUtilityCall(ctx, api.LLMCallEvent{
		CallID:   callID,
		Provider: provider,
		Model:    model,
		Status:   api.LLMCallStatusActive,
	})
	return callID, true
}

func (r *RegistrySummarizer) endLaneCall(ctx context.Context, occupy bool, callID, provider, model string, errp *error) {
	if !occupy {
		return
	}
	status := api.LLMCallStatusOK
	if errp != nil && *errp != nil {
		status = api.LLMCallStatusError
	}
	r.publishUtilityCall(context.WithoutCancel(ctx), api.LLMCallEvent{
		CallID:   callID,
		Provider: provider,
		Model:    model,
		Status:   status,
	})
}

func (r *RegistrySummarizer) planeAcquire(ctx context.Context, p modelcall.Provider, class UtilityClass) (func(), error) {
	if r == nil || r.Plane == nil {
		return func() {}, nil
	}
	return r.Plane.acquireLane(ctx, p, class)
}

func providerIDOf(p modelcall.Provider) string {
	if p == nil {
		return ""
	}
	return p.ID()
}

func (r *RegistrySummarizer) observingLiteSlot(p modelcall.Provider) bool {
	if r == nil || p == nil {
		return false
	}
	lite, _, err := r.liteProvider()
	if err != nil || lite == nil {
		return false
	}
	return lite.ID() == p.ID()
}

// void discards a receipt before generation starts.
func (a *utilityAttempt) void() {
	if a == nil || a.settled {
		return
	}
	a.settled = true
	if ledger, ok := a.ledger(); ok {
		if err := ledger.VoidCall(a.books, a.callID); err != nil {
			slog.Warn("void llm call receipt", "call_id", a.callID, "error", err)
		}
	}
}

// finish records reported or host-measured usage.
func (a *utilityAttempt) finish(req modelcall.CompletionRequest, resp *modelcall.Completion) {
	if a == nil || a.settled {
		return
	}
	a.settled = true
	switch {
	case resp != nil && resp.Usage.Reported():
		a.summarizer.recordSummarizerUsage(a.books, a.callID, a.provider, a.model, resp.Usage, cost.UsageFromProvider)
	case modelcall.CompletionHasPayload(resp):
		a.summarizer.recordSummarizerUsage(a.books, a.callID, a.provider, a.model, MeasureUsage(req, resp), cost.UsageFromHost)
	default:
		a.abandon()
	}
}

// close settles the receipt. The budget's deadline is released by the provider
// that started it.
func (a *utilityAttempt) close() {
	if a == nil {
		return
	}
	if !a.settled {
		a.settled = true
		a.abandon()
	}
}

// abandon marks usage as unknown.
func (a *utilityAttempt) abandon() {
	ledger, ok := a.ledger()
	if !ok {
		return
	}
	if err := ledger.MarkCallUnknown(a.books, a.callID); err != nil {
		slog.Warn("mark llm call unreported", "call_id", a.callID, "error", err)
	}
}

func (a *utilityAttempt) ledger() (cost.CallLedger, bool) {
	if a == nil || strings.TrimSpace(a.callID) == "" || a.summarizer == nil || a.summarizer.Cost == nil {
		return nil, false
	}
	ledger, ok := a.summarizer.Cost.(cost.CallLedger)
	return ledger, ok
}

// openSummarizerReceipt records a call before I/O.
func (r *RegistrySummarizer) openSummarizerReceipt(ctx context.Context, providerID, model string) string {
	if r == nil || r.Cost == nil {
		return ""
	}
	ledger, ok := r.Cost.(cost.CallLedger)
	if !ok {
		return ""
	}
	sess := curationctx.SessionFrom(ctx)
	id := uuid.NewString()
	if err := ledger.BeginCall(ctx, cost.UsageEvent{
		ID: id, SessionID: strings.TrimSpace(sess.SessionID), ParentSessionID: strings.TrimSpace(sess.ParentSessionID),
		ProjectID: strings.TrimSpace(firstNonEmpty(r.ProjectID, sess.ProjectID)), ProviderID: providerID,
		Model: model, Caller: cost.CallerSummarizer, StartedAt: time.Now().UTC(),
	}); err != nil {
		slog.WarnContext(ctx, "open llm call receipt", "provider", providerID, "model", model, "error", err)
		return ""
	}
	return id
}

func (r *RegistrySummarizer) recordSummarizerUsage(
	ctx context.Context, callID, providerID, model string, usage modelcall.TokenUsage, source cost.UsageSource,
) {
	if r == nil || r.Cost == nil {
		return
	}
	sess := curationctx.SessionFrom(ctx)
	sessionID := strings.TrimSpace(sess.SessionID)
	parentID := strings.TrimSpace(sess.ParentSessionID)
	projectID := strings.TrimSpace(r.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(sess.ProjectID)
	}
	est, err := r.Cost.Estimate(ctx, providerID, model, cost.TokenUsage{
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
		slog.WarnContext(ctx, "convert llm call cost", "call_id", callID, "error", err)
		return
	}
	if err := r.Cost.RecordUsage(ctx, cost.UsageEvent{
		ID:                         callID,
		SessionID:                  sessionID,
		ParentSessionID:            parentID,
		ProjectID:                  projectID,
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
		Caller:                     cost.CallerSummarizer,
		UsageSource:                source,
	}); err != nil {
		slog.WarnContext(ctx, "record llm call usage", "call_id", callID, "error", err)
	}
}

// stampSessionAttribution fills missing identity from the ambient curation session.
func stampSessionAttribution(debug modelcall.RequestDebug, sess curationctx.Session) modelcall.RequestDebug {
	debug.SessionID = firstNonEmpty(debug.SessionID, sess.SessionID)
	debug.ProjectID = firstNonEmpty(debug.ProjectID, sess.ProjectID)
	debug.ProjectDir = firstNonEmpty(debug.ProjectDir, sess.ProjectDir)
	debug.AgentType = firstNonEmpty(debug.AgentType, sess.Agent)
	debug.ProfileID = firstNonEmpty(debug.ProfileID, sess.Agent)
	debug.ParentSessionID = firstNonEmpty(debug.ParentSessionID, sess.ParentSessionID)
	return debug
}

// MeasureUsage estimates unreported usage from request and response content.
// The rune-based estimate counts cached prefixes as fresh input.
func MeasureUsage(req modelcall.CompletionRequest, completion *modelcall.Completion) modelcall.TokenUsage {
	usage := modelcall.TokenUsage{PromptTokens: compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(req.Messages))}
	if len(req.Tools) > 0 {
		if raw, err := json.Marshal(req.Tools); err == nil {
			usage.PromptTokens += tokenest.EstimateDefault(string(raw))
		}
	}
	if completion == nil {
		return usage
	}
	usage.CompletionTokens = tokenest.EstimateDefault(completion.Content) + tokenest.EstimateDefault(completion.Reasoning)
	for _, call := range completion.ToolCalls {
		usage.CompletionTokens += tokenest.EstimateDefault(call.Name)
		if len(call.Args) > 0 {
			if raw, err := json.Marshal(call.Args); err == nil {
				usage.CompletionTokens += tokenest.EstimateDefault(string(raw))
			}
		}
	}
	return usage
}
