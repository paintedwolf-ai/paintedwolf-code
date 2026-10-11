package history

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptassembly"
	"github.com/lycaon/lycaon/internal/session/store"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/pkg/api"
)

const UncompactedLimit = 8192

// Load rejects suffixes too large to assemble without gaps.
func (m *Service) Load(ctx context.Context, sess *api.Session) ([]api.Message, error) {
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
		m.Schedule(ctx, sess)
		return m.loadBoundedCanonicalPromptHistory(ctx, sess)
	}
	suffix, err := m.store.GetMessagesAfterOrd(ctx, sess.ID, view.CoveredThroughOrd, UncompactedLimit+1)
	if err != nil {
		return nil, err
	}
	if len(suffix) > UncompactedLimit {
		m.Schedule(ctx, sess)
		return nil, fmt.Errorf("prompt history projection is more than %d rows behind; compaction scheduled", UncompactedLimit)
	}
	return suffix, nil
}

func (m *Service) loadBoundedCanonicalPromptHistory(ctx context.Context, sess *api.Session) ([]api.Message, error) {
	history, err := m.store.GetMessagesAfterOrd(ctx, sess.ID, 0, UncompactedLimit+1)
	if err != nil {
		return nil, err
	}
	if len(history) > UncompactedLimit {
		m.Schedule(ctx, sess)
		return nil, fmt.Errorf("prompt history has more than %d unprojected rows; compaction scheduled", UncompactedLimit)
	}
	return history, nil
}

// Fit assembles prompt history and schedules durable compaction.
func (m *Service) Fit(ctx context.Context, sess *api.Session, history []api.Message, surfaceID string) ([]api.Message, compaction.CompactionReport, error) {
	assembled, report := m.Assemble(ctx, sess, history, surfaceID)
	if m.Compactor != nil {
		cfg := m.Compactor.Config()
		// Per-call fitting does not lower the durable retry threshold.
		est := m.BudgetTokens(sess.ID, report.TokensBefore)
		if est >= cfg.HardCeilingTokens {
			m.Schedule(ctx, sess)
		} else if compaction.ShouldCompactSession(est, cfg.ModelContextWindow, cfg) {
			m.Schedule(ctx, sess)
		}
	}
	return assembled, report.CompactionReport(), nil
}

// ApplyView appends rows beyond the view's ordinal watermark.
func (m *Service) ApplyView(ctx context.Context, sess *api.Session, history []api.Message) []api.Message {
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

func (m *Service) Schedule(ctx context.Context, sess *api.Session) {
	if m == nil || sess == nil || m.Runner == nil || m.Compactor == nil {
		return
	}
	sessionID := sess.ID
	_ = m.Gate.WithSessionTreeAdmission(ctx, sessionID, func() error {
		m.Runner.Trigger(ctx, sessionID, func(bgCtx context.Context) {
			_ = m.Runner.Execute(bgCtx, sessionID, func(runCtx context.Context) error { m.runBackgroundCompaction(runCtx, sessionID); return nil })
		})
		return nil
	})
}

// runBackgroundCompaction writes durable views from canonical history.
func (m *Service) runBackgroundCompaction(ctx context.Context, sessionID string) {
	if m == nil || m.Compactor == nil || m.store == nil {
		return
	}
	sc, ok := m.Compactor.(*compaction.SimpleCompactor)
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

func (m *Service) runBackgroundCompactionPage(ctx context.Context, sessionID string, sc *compaction.SimpleCompactor) (bool, bool) {
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return false, false
	}
	originals, coveredThroughOrd, coveredThroughID, sourceSeq, hasMore, err := m.loadCompactionPage(ctx, sess)
	if err != nil || len(originals) == 0 {
		return false, false
	}
	info := m.sessionInfo(ctx, sess, originals)
	base := api.FilterPromptHistory(promptassembly.MarkContextPins(sess, originals))
	ctxMsgs := compaction.ContextMessagesFromAPI(base)
	report := compaction.CompactionReport{TokensBefore: compaction.EstimateMessagesTokens(ctxMsgs)}

	// Oversized messages compact independently of the session watermark.
	working, chunks := sc.CompactOversizedChunksOnly(ctx, info, ctxMsgs)
	report.ChunksCompacted = chunks
	changed := chunks > 0

	// Full summaries run only above the session watermark.
	cfg := sc.Config()
	budgetEst := m.BudgetTokens(sessionID, compaction.EstimateMessagesTokens(working))
	rowPressure := hasMore || len(working) > UncompactedLimit
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

func (m *Service) loadCompactionPage(ctx context.Context, sess *api.Session) ([]api.Message, int64, string, int64, bool, error) {
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
	suffix, err := m.store.GetMessagesAfterOrd(ctx, sess.ID, coveredThroughOrd, UncompactedLimit+1)
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
	hasMore := len(suffix) > UncompactedLimit
	if hasMore {
		suffix = suffix[:UncompactedLimit]
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
func (m *Service) writeCompactionView(ctx context.Context, sess *api.Session, originals []api.Message, compact []compaction.ContextMessage, report compaction.CompactionReport, coveredThroughOrd int64, coveredThroughID string, sourceSeq int64) error {
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

// ScheduleChunks schedules background chunk compaction.
func (m *Service) ScheduleChunks(ctx context.Context, sess *api.Session) error {
	if m == nil || sess == nil {
		return nil
	}
	m.Schedule(ctx, sess)
	return nil
}

func (m *Service) Wait() {
	if m != nil && m.Runner != nil {
		m.Runner.Wait()
	}
}

// ForceCompact compacts session history to target tokens regardless of trigger
// threshold and stores the result as the session's compacted view.
func (m *Service) ForceCompact(ctx context.Context, sessionID string) (report compaction.CompactionReport, err error) {
	turn, err := m.Gate.Capture(ctx, sessionID)
	if err != nil {
		return report, err
	}
	err = m.Runner.Execute(ctx, sessionID, func(runCtx context.Context) error {
		// Register cancelable work before releasing stop admission; the admission
		// gate is not held across a provider call or a wait for another compaction.
		if err := m.Gate.WithSessionTreeAdmission(runCtx, sessionID, func() error {
			if !m.Gate.MayDrain(turn) {
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

func (m *Service) forceCompact(ctx context.Context, sessionID string) (compaction.CompactionReport, error) {
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return compaction.CompactionReport{}, err
	}
	originals, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return compaction.CompactionReport{}, err
	}
	if m.Compactor == nil {
		return compaction.CompactionReport{TokensBefore: compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(originals))}, fmt.Errorf("compactor not configured")
	}
	cfg := m.Compactor.Config()
	base := api.FilterPromptHistory(promptassembly.MarkContextPins(sess, m.ApplyView(ctx, sess, originals)))
	ctxMsgs := compaction.ContextMessagesFromAPI(base)
	compact, report, err := m.Compactor.Compact(ctx, m.sessionInfo(ctx, sess, originals), ctxMsgs, cfg.TargetTokens)
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

func (m *Service) sessionInfo(ctx context.Context, sess *api.Session, originals []api.Message) compaction.SessionInfo {
	projectDir, _ := m.Workspace.ActivePath(ctx, sess)
	info := compaction.SessionInfo{
		ID:                   sess.ID,
		AgentType:            sess.AgentType,
		ParentSessionID:      sess.ParentSessionID,
		ProjectID:            sess.ProjectID,
		ProjectDir:           projectDir,
		HostDataDir:          project.HostDataDir(m.dataDir, sess.ProjectID),
		MaxToolSpillBytes:    m.Limits.Effective(ctx, sess).MaxToolSpillBytes,
		Posture:              sess.Posture,
		CompactionGeneration: sess.CompactionGeneration,
		RecallAvailable:      m.recallAvailable(ctx, sess),
		ChunkProjections:     m.store,
		Attempts:             m.store,
		Model:                sess.Model,
		ProviderID:           sess.ProviderID,
	}
	info.ProviderID, info.Model = m.Model(ctx, sess)
	if boundary := api.UserIntentBoundary(originals); boundary > 0 {
		info.LatestUserRequestID = originals[boundary-1].ID //nolint:gosec // UserIntentBoundary returns zero or an index within originals.
	}
	if m.progress != nil {
		rootID := sess.ID
		if sess.IsWorkerChild() {
			rootID = sessiontree.RootID(ctx, m.store, sess.ID)
		}
		info.ProgressMarkdown = m.progress.Get(ctx, rootID)
	}
	return info
}

// model uses the same routed selection as the receiving coordinator.
func (m *Service) Model(ctx context.Context, sess *api.Session) (string, string) {
	if m.router != nil {
		selection, err := m.router.WithOverlayRoots(m.Workspace.SettingsRoots(ctx, sess)).ResolveSession(ctx, sess)
		if err == nil && selection != nil {
			return selection.ProviderID, selection.Model
		}
	}
	return sess.ProviderID, sess.Model
}

// SessionContext returns debug context stats for a session, measured against the
// compacted view the model actually sees.
func (m *Service) SessionContext(ctx context.Context, sessionID string) (api.SessionContextResponse, error) {
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil {
		return api.SessionContextResponse{}, err
	}
	history, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return api.SessionContextResponse{}, err
	}
	_, report := m.Assemble(ctx, sess, history, "")
	est := m.BudgetTokens(sessionID, report.TokensAfter)
	remaining := 0
	if m.Compactor != nil {
		remaining, _ = m.Compactor.BudgetRemaining(ctx, sessionID, est)
	}
	cfg := compaction.DefaultCompactionConfig()
	if m.Compactor != nil {
		cfg = m.Compactor.Config()
	}
	return api.SessionContextResponse{
		EstimatedTokens:      est,
		BudgetRemaining:      remaining,
		CompactionGeneration: sess.CompactionGeneration,
		OversizedChunks:      len(compaction.FindOversizedChunks(compaction.ContextMessagesFromAPI(history), cfg, nil)),
	}, nil
}
