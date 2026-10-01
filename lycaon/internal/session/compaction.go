package session

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptassembly"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

const promptHistoryUncompactedLimit = 8192

// loadPromptHistory rejects suffixes too large to assemble without gaps.
func (m *Manager) loadPromptHistory(ctx context.Context, sess *api.Session) ([]api.Message, error) {
	if m == nil || m.store == nil || sess == nil {
		return nil, fmt.Errorf("session store and session are required")
	}
	view, ok, err := m.store.GetCompactionView(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	if !ok || view.Generation != sess.CompactionGeneration {
		return m.loadBoundedCanonicalPromptHistory(ctx, sess)
	}
	current, err := m.store.CompactionViewCurrent(ctx, sess.ID, view.CoveredThroughOrd, view.CoveredThroughID, view.SourceSeq)
	if err != nil {
		return nil, err
	}
	if !current {
		m.triggerBackgroundCompaction(ctx, sess)
		return m.loadBoundedCanonicalPromptHistory(ctx, sess)
	}
	suffix, err := m.store.GetMessagesAfterOrd(ctx, sess.ID, view.CoveredThroughOrd, promptHistoryUncompactedLimit+1)
	if err != nil {
		return nil, err
	}
	if len(suffix) > promptHistoryUncompactedLimit {
		m.triggerBackgroundCompaction(ctx, sess)
		return nil, fmt.Errorf("prompt history projection is more than %d rows behind; compaction scheduled", promptHistoryUncompactedLimit)
	}
	return suffix, nil
}

func (m *Manager) loadBoundedCanonicalPromptHistory(ctx context.Context, sess *api.Session) ([]api.Message, error) {
	history, err := m.store.GetMessagesAfterOrd(ctx, sess.ID, 0, promptHistoryUncompactedLimit+1)
	if err != nil {
		return nil, err
	}
	if len(history) > promptHistoryUncompactedLimit {
		m.triggerBackgroundCompaction(ctx, sess)
		return nil, fmt.Errorf("prompt history has more than %d unprojected rows; compaction scheduled", promptHistoryUncompactedLimit)
	}
	return history, nil
}

// maybeCompact assembles prompt history and schedules durable compaction.
func (m *Manager) maybeCompact(ctx context.Context, sess *api.Session, history []api.Message, surfaceID string) ([]api.Message, compaction.CompactionReport, error) {
	assembled, report := m.assemblePromptHistory(ctx, sess, history, surfaceID)
	if m.compactor != nil {
		cfg := m.compactor.Config()
		// Per-call fitting does not lower the durable retry threshold.
		est := m.compactionBudgetTokens(sess.ID, report.TokensBefore)
		if est >= cfg.HardCeilingTokens {
			m.triggerBackgroundCompaction(ctx, sess)
		} else if compaction.ShouldCompactSession(est, cfg.ModelContextWindow, cfg) {
			m.triggerBackgroundCompaction(ctx, sess)
		}
	}
	return assembled, report.CompactionReport(), nil
}

// applyCompactionView appends rows beyond the view's ordinal watermark.
func (m *Manager) applyCompactionView(ctx context.Context, sess *api.Session, history []api.Message) []api.Message {
	if m == nil || m.store == nil || sess == nil {
		return history
	}
	view, ok, err := m.store.GetCompactionView(ctx, sess.ID)
	if err != nil || !ok || view.Generation != sess.CompactionGeneration {
		return history
	}
	current, currentErr := m.store.CompactionViewCurrent(ctx, sess.ID, view.CoveredThroughOrd, view.CoveredThroughID, view.SourceSeq)
	if currentErr != nil || !current {
		return history
	}
	byID := make(map[string]api.Message, len(history))
	for _, msg := range history {
		byID[msg.ID] = msg
	}
	out := make([]api.Message, 0, len(view.Messages)+8)
	for _, msg := range view.Messages {
		if canonical, ok := byID[msg.ID]; ok {
			// Canonical rows restore fields excluded from prompt projections.
			msg = api.RehydrateTranscriptProjectionFields(msg, canonical)
		}
		out = append(out, msg)
	}
	for _, msg := range history {
		if msg.Ord <= view.CoveredThroughOrd {
			continue
		}
		out = append(out, msg)
	}
	return out
}

func (m *Manager) triggerBackgroundCompaction(ctx context.Context, sess *api.Session) {
	if m == nil || sess == nil || m.compactionRunner == nil || m.compactor == nil {
		return
	}
	sessionID := sess.ID
	_ = m.WithSessionTreeAdmission(ctx, sessionID, func() error {
		m.compactionRunner.Trigger(ctx, sessionID, func(bgCtx context.Context) {
			_ = m.compactionRunner.Execute(bgCtx, sessionID, func(runCtx context.Context) error { m.runBackgroundCompaction(runCtx, sessionID); return nil })
		})
		return nil
	})
}

// runBackgroundCompaction writes durable views from canonical history.
func (m *Manager) runBackgroundCompaction(ctx context.Context, sessionID string) {
	if m == nil || m.compactor == nil || m.store == nil {
		return
	}
	sc, ok := m.compactor.(*compaction.SimpleCompactor)
	if !ok {
		return
	}
	for ctx.Err() == nil {
		hasMore, wrote := m.runBackgroundCompactionPage(ctx, sessionID, sc)
		if !hasMore || !wrote {
			return
		}
	}
}

func (m *Manager) runBackgroundCompactionPage(ctx context.Context, sessionID string, sc *compaction.SimpleCompactor) (bool, bool) {
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return false, false
	}
	originals, coveredThroughOrd, coveredThroughID, sourceSeq, hasMore, err := m.loadCompactionPage(ctx, sess)
	if err != nil || len(originals) == 0 {
		return false, false
	}
	info := m.compactionSessionInfo(ctx, sess, originals)
	base := api.FilterPromptHistory(promptassembly.MarkContextPins(sess, originals))
	ctxMsgs := compaction.ContextMessagesFromAPI(base)
	report := compaction.CompactionReport{TokensBefore: compaction.EstimateMessagesTokens(ctxMsgs)}

	// Oversized messages compact independently of the session watermark.
	working, chunks := sc.CompactOversizedChunksOnly(ctx, info, ctxMsgs)
	report.ChunksCompacted = chunks
	changed := chunks > 0

	// Full summaries run only above the session watermark.
	cfg := sc.Config()
	budgetEst := m.compactionBudgetTokens(sessionID, compaction.EstimateMessagesTokens(working))
	rowPressure := hasMore || len(working) > promptHistoryUncompactedLimit
	if compaction.ShouldCompactSession(budgetEst, cfg.ModelContextWindow, cfg) || rowPressure {
		maxTokens := cfg.TargetTokens
		if rowPressure && compaction.EstimateMessagesTokens(working) <= maxTokens {
			maxTokens = compaction.EstimateMessagesTokens(working) - 1
			if maxTokens < 1 {
				maxTokens = 1
			}
		}
		summarized, sessionReport, cerr := sc.Compact(ctx, info, working, maxTokens)
		if cerr != nil {
			slog.WarnContext(ctx, "background session compaction failed",
				"session_id", sessionID, "error", cerr)
		}
		if cerr == nil && (sessionReport.SessionCompacted || sessionReport.ChunksCompacted > 0) {
			working = summarized
			report.SessionCompacted = sessionReport.SessionCompacted
			report.ChunksCompacted += sessionReport.ChunksCompacted
			changed = true
		}
	}
	if !changed {
		return hasMore, false
	}
	if ctx.Err() != nil {
		return false, false
	}
	report.TokensAfter = compaction.EstimateMessagesTokens(working)
	if err := m.writeCompactionView(ctx, sess, originals, working, report, coveredThroughOrd, coveredThroughID, sourceSeq); err != nil {
		return false, false
	}
	return hasMore, true
}

func (m *Manager) loadCompactionPage(ctx context.Context, sess *api.Session) ([]api.Message, int64, string, int64, bool, error) {
	var base []api.Message
	var coveredThroughOrd, sourceSeq int64
	var coveredThroughID string
	view, ok, err := m.store.GetCompactionView(ctx, sess.ID)
	if err != nil {
		return nil, 0, "", 0, false, err
	}
	if ok && view.Generation == sess.CompactionGeneration {
		current, currentErr := m.store.CompactionViewCurrent(ctx, sess.ID, view.CoveredThroughOrd, view.CoveredThroughID, view.SourceSeq)
		if currentErr != nil {
			return nil, 0, "", 0, false, currentErr
		}
		if current {
			base = append(base, view.Messages...)
			coveredThroughOrd = view.CoveredThroughOrd
			coveredThroughID = view.CoveredThroughID
			sourceSeq = view.SourceSeq
		}
	}
	suffix, err := m.store.GetMessagesAfterOrd(ctx, sess.ID, coveredThroughOrd, promptHistoryUncompactedLimit+1)
	if err != nil {
		return nil, 0, "", 0, false, err
	}
	if len(base) > 0 {
		// Recheck after reading the suffix: its newer rows would otherwise mask
		// an edit to the compacted prefix made in between.
		current, err := m.store.CompactionViewCurrent(ctx, sess.ID, coveredThroughOrd, coveredThroughID, sourceSeq)
		if err != nil {
			return nil, 0, "", 0, false, err
		}
		if !current {
			return nil, 0, "", 0, false, fmt.Errorf("compaction source changed while reading history")
		}
	}
	hasMore := len(suffix) > promptHistoryUncompactedLimit
	if hasMore {
		suffix = suffix[:promptHistoryUncompactedLimit]
	}
	for _, msg := range suffix {
		if msg.Ord > coveredThroughOrd {
			coveredThroughOrd = msg.Ord
			coveredThroughID = msg.ID
		}
		if msg.Seq > sourceSeq {
			sourceSeq = msg.Seq
		}
	}
	return append(base, suffix...), coveredThroughOrd, coveredThroughID, sourceSeq, hasMore, nil
}

// writeCompactionView stores a generation and its covered canonical messages.
func (m *Manager) writeCompactionView(ctx context.Context, sess *api.Session, originals []api.Message, compact []compaction.ContextMessage, report compaction.CompactionReport, coveredThroughOrd int64, coveredThroughID string, sourceSeq int64) error {
	generation := sess.CompactionGeneration + 1
	view := store.CompactionView{
		Generation:        generation,
		Messages:          compaction.ContextMessagesToAPI(compact, originals),
		CoveredThroughOrd: coveredThroughOrd,
		CoveredThroughID:  coveredThroughID,
		SourceSeq:         sourceSeq,
		TokensBefore:      report.TokensBefore,
		TokensAfter:       report.TokensAfter,
	}

	if err := m.store.PutCompactionView(ctx, sess.ID, view); err != nil {
		return err
	}
	slog.DebugContext(ctx, "compaction generation applied", "session_id", sess.ID, "generation", generation,
		"chunks_compacted", report.ChunksCompacted, "session_compacted", report.SessionCompacted,
		"tokens_before", report.TokensBefore, "tokens_after", report.TokensAfter)
	return nil
}

// compactOversizedToolResultsInSession schedules background chunk compaction.
func (m *Manager) compactOversizedToolResultsInSession(ctx context.Context, sess *api.Session) error {
	if m == nil || sess == nil {
		return nil
	}
	m.triggerBackgroundCompaction(ctx, sess)
	return nil
}

func (m *Manager) waitForCompaction() {
	if m != nil && m.compactionRunner != nil {
		m.compactionRunner.Wait()
	}
}

// ForceCompact compacts session history to target tokens regardless of trigger
// threshold and stores the result as the session's compacted view.
func (m *Manager) ForceCompact(ctx context.Context, sessionID string) (report compaction.CompactionReport, err error) {
	turn, err := m.captureSessionTurn(ctx, sessionID)
	if err != nil {
		return report, err
	}
	err = m.compactionRunner.Execute(ctx, sessionID, func(runCtx context.Context) error {
		// Register cancelable work before releasing stop admission; the admission
		// gate is not held across a provider call or a wait for another compaction.
		if err := m.WithSessionTreeAdmission(runCtx, sessionID, func() error {
			if !m.stopState.MayDrain(turn) {
				return lifecycle.ErrStopping
			}
			return nil
		}); err != nil {
			return err
		}
		var compactErr error
		report, compactErr = m.forceCompact(runCtx, sessionID)
		return compactErr
	})
	return report, err
}

func (m *Manager) forceCompact(ctx context.Context, sessionID string) (compaction.CompactionReport, error) {
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return compaction.CompactionReport{}, err
	}
	originals, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return compaction.CompactionReport{}, err
	}
	if m.compactor == nil {
		return compaction.CompactionReport{TokensBefore: compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(originals))}, fmt.Errorf("compactor not configured")
	}
	cfg := m.compactor.Config()
	base := api.FilterPromptHistory(promptassembly.MarkContextPins(sess, m.applyCompactionView(ctx, sess, originals)))
	ctxMsgs := compaction.ContextMessagesFromAPI(base)
	compact, report, err := m.compactor.Compact(ctx, m.compactionSessionInfo(ctx, sess, originals), ctxMsgs, cfg.TargetTokens)
	report.CompactionGeneration = sess.CompactionGeneration
	if err != nil {
		return report, err
	}
	if !report.SessionCompacted && report.ChunksCompacted == 0 {
		return report, nil
	}
	coveredThroughOrd, coveredThroughID, sourceSeq := messageWatermarks(originals)
	if err := m.writeCompactionView(ctx, sess, originals, compact, report, coveredThroughOrd, coveredThroughID, sourceSeq); err != nil {
		return report, err
	}
	report.CompactionGeneration = sess.CompactionGeneration + 1
	return report, nil
}

func messageWatermarks(messages []api.Message) (coveredThroughOrd int64, coveredThroughID string, sourceSeq int64) {
	for _, msg := range messages {
		if msg.Ord > coveredThroughOrd {
			coveredThroughOrd = msg.Ord
			coveredThroughID = msg.ID
		}
		if msg.Seq > sourceSeq {
			sourceSeq = msg.Seq
		}
	}
	return coveredThroughOrd, coveredThroughID, sourceSeq
}

func (m *Manager) compactionSessionInfo(ctx context.Context, sess *api.Session, originals []api.Message) compaction.SessionInfo {
	projectDir, _ := m.sessionActiveRootPath(ctx, sess)
	info := compaction.SessionInfo{
		ID:                   sess.ID,
		AgentType:            sess.AgentType,
		ParentSessionID:      sess.ParentSessionID,
		ProjectID:            sess.ProjectID,
		ProjectDir:           projectDir,
		HostDataDir:          m.HostDataDirFor(sess.ProjectID),
		MaxToolSpillBytes:    m.effectiveLimits(ctx, sess).MaxToolSpillBytes,
		Posture:              sess.Posture,
		CompactionGeneration: sess.CompactionGeneration,
		RecallAvailable:      m.recallAvailable(ctx, sess),
		ChunkProjections:     m.store,
		Attempts:             m.store,
		Model:                sess.Model,
		ProviderID:           sess.ProviderID,
	}
	info.ProviderID, info.Model = m.compactionModel(ctx, sess)
	if boundary := api.UserIntentBoundary(originals); boundary > 0 {
		info.LatestUserRequestID = originals[boundary-1].ID //nolint:gosec // UserIntentBoundary returns zero or an index within originals.
	}
	if m.progress != nil {
		rootID := sess.ID
		if sess.IsWorkerChild() {
			rootID = RootSessionID(ctx, m.store, sess.ID)
		}
		info.ProgressMarkdown = m.progress.Get(ctx, rootID)
	}
	return info
}

// compactionModel uses the same routed selection as the receiving coordinator.
func (m *Manager) compactionModel(ctx context.Context, sess *api.Session) (string, string) {
	if m.llmSvc != nil && m.llmSvc.Router != nil {
		selection, err := m.llmSvc.Router.WithOverlayRoots(m.overlayRootPaths(ctx, sess)).ResolveSession(ctx, sess)
		if err == nil && selection != nil {
			return selection.ProviderID, selection.Model
		}
	}
	return sess.ProviderID, sess.Model
}

// recallAvailable reports whether the session's prompt profile carries recall.
// The continuation record names the tool only when the model can call it.
func (m *Manager) recallAvailable(ctx context.Context, sess *api.Session) bool {
	profileID, err := m.promptToolProfile(ctx, sess)
	if err != nil {
		return false
	}
	profile, ok := m.toolProfileByID(ctx, sess, profileID)
	return ok && profile.ToolAllowed(recall.ToolName)
}

// SessionContext returns debug context stats for a session, measured against the
// compacted view the model actually sees.
func (m *Manager) SessionContext(ctx context.Context, sessionID string) (api.SessionContextResponse, error) {
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return api.SessionContextResponse{}, err
	}
	history, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return api.SessionContextResponse{}, err
	}
	_, report := m.assemblePromptHistory(ctx, sess, history, "")
	est := m.compactionBudgetTokens(sessionID, report.TokensAfter)
	remaining := 0
	if m.compactor != nil {
		remaining, _ = m.compactor.BudgetRemaining(ctx, sessionID, est)
	}
	cfg := compaction.DefaultCompactionConfig()
	if m.compactor != nil {
		cfg = m.compactor.Config()
	}
	return api.SessionContextResponse{
		EstimatedTokens:      est,
		BudgetRemaining:      remaining,
		CompactionGeneration: sess.CompactionGeneration,
		OversizedChunks:      len(compaction.FindOversizedChunks(compaction.ContextMessagesFromAPI(history), cfg, nil)),
	}, nil
}
