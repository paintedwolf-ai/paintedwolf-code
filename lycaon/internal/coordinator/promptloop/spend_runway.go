package promptloop

import (
	"context"
	"strings"
	"log/slog"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

// SpendRunway reports proximity to the session spend ceiling.
type SpendRunway struct {
	Low        bool
	CeilingUSD float64
}

// SpendCeilingCheck reports a warning or permission for one final tool round.
type SpendCeilingCheck struct {
	Runway   SpendRunway
	SoftStop bool
}

// SpendRunwayNudge renders the first spend warning.
type SpendRunwayNudge func(ctx context.Context, sess *api.Session, ceilingUSD float64) HostNudge

// SpendSoftStopNudge renders instructions for the final tool round.
type SpendSoftStopNudge func(ctx context.Context, sess *api.Session) HostNudge

// spendCeilingDecision selects a warning, final tool round, or completed closeout.
type spendCeilingDecision struct {
	Runway   SpendRunway
	WindDown bool
	Finished *PromptRunResult
}

// A spent session may receive one final tool round before closeout.
func (l turnNudges) applySpendCeiling(
	ctx context.Context,
	sess *api.Session,
	sessionID, profileID, userPrompt string,
	maxIter int,
	in PromptRunInput,
	st *promptLoopTurnState,
) (spendCeilingDecision, error) {
	var out spendCeilingDecision
	if l.PromptLoop == nil || l.Deps.CheckSpendCeiling == nil {
		return out, nil
	}
	check, err := l.Deps.CheckSpendCeiling(ctx, sessionID, sess)
	if err == nil {
		out.Runway = check.Runway
		return out, nil
	}
	if !l.spendCeilingReached(err) || strings.TrimSpace(st.lastAssistantID) == "" {
		return out, err
	}
	if sess != nil && !sess.IsWorkerChild() && !st.spendSoftStopGranted && check.SoftStop {
		st.spendSoftStopGranted = true
		out.WindDown = true
		return out, nil
	}
	closedHistory, aid, content, cerr := turnCloseout(l).runEarlyTurnCloseout(
		ctx, sess, sessionID, profileID, userPrompt, st.history, maxIter,
		TurnCloseoutSpendCeiling, "", false, in, st,
	)
	if cerr != nil {
		return out, cerr
	}
	if aid == "" {
		return out, err
	}
	st.history = closedHistory
	out.Finished = &PromptRunResult{
		LastAssistantID:      aid,
		LastOutputID:         st.lastOutputID,
		LastAssistantContent: content,
		TurnTools:            st.turnTools,
		TasksDispatchedCount: st.tasksDispatchedCount,
	}
	return out, nil
}

func (l turnNudges) maybeSpendSoftStopNudge(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	windDown bool,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	if l.PromptLoop == nil || l.Deps.SpendSoftStopNudge == nil || sess == nil || !windDown {
		return history, nil
	}
	nudge := l.Deps.SpendSoftStopNudge(ctx, sess)
	if nudge.Empty() {
		return history, nil
	}
	return l.appendHostNudge(ctx, sessionID, history, nudge, "", st)
}

func filterSpendSoftStopToolCalls(calls []api.ToolCall) []api.ToolCall {
	if len(calls) == 0 {
		return calls
	}
	out := make([]api.ToolCall, 0, len(calls))
	for _, call := range calls {
		if isTaskToolName(call.Name) {
			continue
		}
		out = append(out, call)
	}
	return out
}

func filterSpendSoftStopToolMetas(metas []tools.ToolMeta) []tools.ToolMeta {
	if len(metas) == 0 {
		return metas
	}
	out := make([]tools.ToolMeta, 0, len(metas))
	for _, meta := range metas {
		if isTaskToolName(meta.Name) {
			continue
		}
		out = append(out, meta)
	}
	return out
}

func (l turnNudges) maybeSpendRunwayNudge(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	runway SpendRunway,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	if l.PromptLoop == nil || l.Deps.SpendRunwayNudge == nil || sess == nil {
		return history, nil
	}
	if !runway.Low {
		return history, nil
	}
	nudge := l.Deps.SpendRunwayNudge(ctx, sess, runway.CeilingUSD)
	if nudge.Empty() {
		return history, nil
	}
	return l.appendHostNudge(ctx, sessionID, history, nudge, "", st)
}

func (l turnNudges) spendCeilingReached(err error) bool {
	if err == nil {
		return false
	}
	if l.PromptLoop != nil && l.Deps.IsSpendCeiling != nil {
		return l.Deps.IsSpendCeiling(err)
	}
	return false
}

func (l modelTurn) voidCostReceipt(ctx context.Context, callID string) {
	ledger, ok := l.Deps.Cost.(cost.CallLedger)
	if !ok || strings.TrimSpace(callID) == "" {
		return
	}
	if err := ledger.VoidCall(ctx, callID); err != nil {
		slog.WarnContext(ctx, "void llm call receipt", "call_id", callID, "error", err)
	}
}

// publishLLMCallOK reports a completed coordinator call.
func (l modelTurn) publishLLMCallOK(
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

func (l modelTurn) resolveUsageMeta(sess *api.Session, completion *modelcall.Completion) (providerID, model string) {
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
func (l modelTurn) recordUsage(
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
func (l modelTurn) costRollupScopeID(ctx context.Context, sess *api.Session) string {
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
func (l modelTurn) recordAbandonedTurnUsage(
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
