package promptloop

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

type sessionLiveWindowTracker struct {
	mu      sync.RWMutex
	windows map[string]int
}

func (t *sessionLiveWindowTracker) lower(sessionID string, window int) {
	sessionID = strings.TrimSpace(sessionID)
	if t == nil || sessionID == "" || window <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.windows == nil {
		t.windows = make(map[string]int)
	}
	if cur, ok := t.windows[sessionID]; !ok || window < cur {
		t.windows[sessionID] = window
	}
}

func (t *sessionLiveWindowTracker) get(sessionID string) (int, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if t == nil || sessionID == "" {
		return 0, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	w, ok := t.windows[sessionID]
	return w, ok && w > 0
}

func (l *modelTurn) lowerSessionLiveWindow(sessionID string, window int) {
	if l == nil || window <= 0 {
		return
	}
	l.liveWindows.lower(sessionID, window)
	if l.Deps.LowerSessionLiveWindow != nil {
		l.Deps.LowerSessionLiveWindow(sessionID, window)
	}
}

func (l *modelTurn) sessionLiveWindow(sessionID string) (int, bool) {
	if l == nil {
		return 0, false
	}
	return l.liveWindows.get(sessionID)
}

func (l *modelTurn) effectiveCompactionConfig(ctx context.Context, sess *api.Session) (compaction.CompactionConfig, bool) {
	var cfg compaction.CompactionConfig
	hasConfig := false
	if l != nil && l.Deps.CompactionConfig != nil {
		cfg = l.Deps.CompactionConfig(ctx, sess)
		hasConfig = true
	}
	var sessionID string
	if sess != nil {
		sessionID = sess.ID
	}
	if liveWindow, ok := l.sessionLiveWindow(sessionID); ok && liveWindow > 0 {
		if !hasConfig {
			cfg = compaction.DefaultCompactionConfig()
			hasConfig = true
		}
		if cfg.ModelContextWindow <= 0 || liveWindow < cfg.ModelContextWindow {
			cfg.ModelContextWindow = liveWindow
			cfg.HardCeilingTokens = 0
			cfg.TargetTokens = 0
			if normalized, err := compaction.NormalizeConfig(cfg); err == nil {
				cfg = normalized
			} else {
				cfg.HardCeilingTokens = liveWindow * 85 / 100
				cfg.TargetTokens = liveWindow * 35 / 100
			}
		}
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
	if tooSmall == nil || tooSmall.MaxContext <= 0 {
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
	if l.Deps.LLMService != nil && l.Deps.LLMService.Registry != nil && providerID != "" && model != "" {
		llm.RecordContextWindow(l.Deps.LLMService.Registry, providerID, model, reportedWindow, tooSmall.Source, true)
	}
	if tooSmall.PromptTokens > 0 && transcriptEstimate > 0 && l.Deps.RecordCompactionTokenObservation != nil {
		l.Deps.RecordCompactionTokenObservation(sessionID, providerID, model, tooSmall.PromptTokens, transcriptEstimate)
	}
	l.lowerSessionLiveWindow(sessionID, reportedWindow)

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
	if len(history) == 0 || reportedWindow <= 0 {
		return history
	}
	cfg, ok := l.effectiveCompactionConfig(ctx, sess)
	if !ok {
		cfg = compaction.DefaultCompactionConfig()
		cfg.ModelContextWindow = reportedWindow
		cfg.HardCeilingTokens = 0
		cfg.TargetTokens = 0
		if normalized, err := compaction.NormalizeConfig(cfg); err == nil {
			cfg = normalized
		}
	}
	targetTokens := cfg.TargetTokens
	if targetTokens <= 0 || targetTokens >= reportedWindow {
		targetTokens = max(128, reportedWindow*35/100)
	}
	ctxMsgs := compaction.ContextMessagesFromAPI(history)
	beforeTokens := compaction.EstimateMessagesTokens(ctxMsgs)
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

func (l *modelTurn) fitRequestToLiveWindow(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	req modelcall.CompletionRequest,
) modelcall.CompletionRequest {
	liveWindow, ok := l.sessionLiveWindow(sessionID)
	if !ok || liveWindow <= 0 {
		return req
	}
	headroom := min(1024, max(0, liveWindow/8))
	reserve := req.MaxTokens
	if reserve <= 0 {
		reserve = max(128, min(4096, liveWindow/8))
	}
	ceiling := max(64, liveWindow-headroom-reserve)
	if modelcall.EstimateRequestInput(req) <= ceiling {
		if req.MaxTokens > 0 && modelcall.EstimateRequestInput(req)+req.MaxTokens+headroom > liveWindow {
			req.MaxTokens = max(128, liveWindow-headroom-modelcall.EstimateRequestInput(req))
		}
		return req
	}
	cfg, hasCfg := l.effectiveCompactionConfig(ctx, sess)
	if !hasCfg {
		cfg = compaction.DefaultCompactionConfig()
	}
	if cfg.KeepRecentMessages > 4 {
		cfg.KeepRecentMessages = 4
	}
	ctxMsgs := compaction.ContextMessagesFromAPI(req.Messages)
	fitted := compaction.DeterministicFit(cfg, ctxMsgs, ceiling)
	if len(fitted) > 0 {
		req.Messages = compaction.ContextMessagesToAPI(fitted, req.Messages)
	}
	if req.MaxTokens <= 0 {
		if avail := liveWindow - headroom - modelcall.EstimateRequestInput(req); avail >= 128 {
			req.MaxTokens = avail
		}
	}
	return req
}
