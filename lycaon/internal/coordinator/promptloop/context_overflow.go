package promptloop

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

func (l *modelTurn) effectiveCompactionConfig(ctx context.Context, sess *api.Session) (compaction.CompactionConfig, bool) {
	var cfg compaction.CompactionConfig
	hasConfig := false
	if l != nil && l.Deps.CompactionConfig != nil {
		cfg = l.Deps.CompactionConfig(ctx, sess)
		hasConfig = true
	}
	return cfg, hasConfig
}

func (l *modelTurn) recoverFromContextOverflow(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	st *promptLoopTurnState,
	surfaceID string,
	callProvider, callModel string,
	transcriptEstimate int,
	tooSmall *failure.ProviderContextTooSmallError,
) []api.Message {
	if tooSmall == nil {
		return history
	}
	reportedWindow := tooSmall.MaxContext
	providerID := strings.TrimSpace(tooSmall.ProviderID)
	if providerID == "" {
		providerID = strings.TrimSpace(callProvider)
	}
	model := strings.TrimSpace(tooSmall.Model)
	if model == "" {
		model = strings.TrimSpace(callModel)
	}
	if tooSmall.PromptTokens > 0 && transcriptEstimate > 0 && l.Deps.RecordCompactionTokenObservation != nil {
		l.Deps.RecordCompactionTokenObservation(sessionID, providerID, model, tooSmall.PromptTokens, transcriptEstimate)
	}

	compacted := history
	if l.Deps.CompactAfterContextOverflow != nil {
		if updated, err := l.Deps.CompactAfterContextOverflow(ctx, sess, compacted, reportedWindow); err == nil && len(updated) > 0 {
			compacted = updated
		}
	} else if l.Tools != nil && l.Tools.Deps.CompactOversizedToolResults != nil {
		_ = l.Tools.Deps.CompactOversizedToolResults(ctx, sessionID, sess)
		if l.Tools.Deps.ReloadHistory != nil {
			if reloaded, err := l.Tools.Deps.ReloadHistory(ctx, sessionID, sess, surfaceID); err == nil && len(reloaded) > 0 {
				compacted = reloaded
			}
		}
	}
	compacted = messageview.PreserveRequestIntent(compacted, history)
	compacted = l.compactHistoryToWindow(ctx, sess, sessionID, compacted, reportedWindow)
	if st != nil {
		st.history = compacted
	}
	return compacted
}

func (l *modelTurn) compactHistoryToWindow(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	reportedWindow int,
) []api.Message {
	if len(history) == 0 {
		return history
	}
	cfg, ok := l.effectiveCompactionConfig(ctx, sess)
	if !ok {
		cfg = compaction.DefaultCompactionConfig()
		if reportedWindow > 0 {
			cfg.ModelContextWindow = reportedWindow
		}
		cfg.HardCeilingTokens = 0
		cfg.TargetTokens = 0
		if normalized, err := compaction.NormalizeConfig(cfg); err == nil {
			cfg = normalized
		}
	}
	targetTokens := cfg.TargetTokens
	if reportedWindow > 0 && (targetTokens <= 0 || targetTokens >= reportedWindow) {
		targetTokens = max(128, reportedWindow*35/100)
	}
	history = messageview.PreserveRequestIntent(history, history)
	ctxMsgs := compaction.ContextMessagesFromAPI(history)
	beforeTokens := compaction.EstimateMessagesTokens(ctxMsgs)
	if reportedWindow <= 0 {
		// A typed rejection without a capacity still permits one bounded reduction.
		// Diagnostic numbers never become serving-window facts.
		targetTokens = max(1, beforeTokens/2)
	}
	compactor := compaction.NewSimpleCompactor(cfg, nil).Baseline()
	info := compaction.SessionInfo{
		ID: sessionID,
	}
	if sess != nil {
		info.AgentType = sess.AgentType
		info.ParentSessionID = sess.ParentSessionID
		info.ProjectID = sess.ProjectID
		info.Model = sess.Model
		info.ProviderID = sess.ProviderID
	}
	maxTokens := targetTokens
	if beforeTokens > 0 && beforeTokens <= maxTokens {
		maxTokens = max(64, beforeTokens/2)
	}
	if working, report, err := compactor.Compact(ctx, info, ctxMsgs, maxTokens); err == nil && (report.SessionCompacted || report.ChunksCompacted > 0 || len(working) < len(ctxMsgs)) {
		ctxMsgs = working
	}
	if compaction.EstimateMessagesTokens(ctxMsgs) > maxTokens || len(ctxMsgs) == len(history) && len(ctxMsgs) > 2 {
		fitCfg := cfg
		if fitCfg.KeepRecentMessages <= 0 || fitCfg.KeepRecentMessages >= len(ctxMsgs) {
			fitCfg.KeepRecentMessages = max(1, len(ctxMsgs)/2)
		}
		fitted := compaction.DeterministicFit(fitCfg, ctxMsgs, maxTokens)
		if len(fitted) > 0 {
			ctxMsgs = fitted
		}
	}
	return compaction.ContextMessagesToAPI(ctxMsgs, history)
}
