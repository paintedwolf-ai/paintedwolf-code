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
	"github.com/lycaon/lycaon/pkg/api"
)

const UncompactedLimit = 256

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
	for pass := 0; pass < 8 && ctx.Err() == nil; pass++ {
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
	sc = sc.WithConfig(m.compactionConfig(ctx, sess))
	base := api.FilterPromptHistory(promptassembly.MarkContextPins(sess, originals, min(8192, max(256, sc.Config().HardCeilingTokens/5))))
	ctxMsgs := compaction.ContextMessagesFromAPI(base)
	report := compaction.CompactionReport{TokensBefore: compaction.EstimateMessagesTokens(ctxMsgs)}

	// Oversized messages compact independently of the session watermark.
	working, chunks := sc.CompactOversizedChunksOnly(ctx, info, ctxMsgs)
	report.ChunksCompacted = chunks
	changed := chunks > 0

	// Full summaries run only above the session watermark.
	cfg := sc.Config()
	bounds := m.canonicalTailBounds(cfg)
	budgetEst := m.BudgetTokens(ctx, sess, compaction.EstimateMessagesTokens(working))
	rowPressure := hasMore || len(working) > bounds.maxRows
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
			report.Continuation = sessionReport.Continuation
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
	return hasMore || (report.SessionCompacted && report.TokensAfter > cfg.TargetTokens), true
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
	bounds := m.canonicalTailBounds(m.compactionConfig(ctx, sess))
	suffix, hasMore, err := m.readBoundedSuffix(ctx, sess.ID, coveredThroughOrd, bounds)
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
		Continuation:      report.Continuation,
		Generation:        generation,
		Messages:          compaction.ContextMessagesToAPI(compact, originals),
		CoveredThroughOrd: coveredThroughOrd,
		CoveredThroughID:  coveredThroughID,
		SourceSeq:         sourceSeq,
		TokensBefore:      report.TokensBefore,
		TokensAfter:       report.TokensAfter,
	}

	if view.Continuation == nil {
		if prior, ok, err := m.store.GetCompactionView(ctx, sess.ID); err == nil && ok {
			view.Continuation = prior.Continuation
		}
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
	originals, coveredThroughOrd, coveredThroughID, sourceSeq, _, err := m.loadCompactionPage(ctx, sess)
	if err != nil {
		return compaction.CompactionReport{}, err
	}
	if m.Compactor == nil {
		return compaction.CompactionReport{TokensBefore: compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(originals))}, fmt.Errorf("compactor not configured")
	}
	cfg := m.compactionConfig(ctx, sess)
	base := api.FilterPromptHistory(promptassembly.MarkContextPins(sess, m.ApplyView(ctx, sess, originals), min(8192, max(256, cfg.HardCeilingTokens/5))))
	ctxMsgs := compaction.ContextMessagesFromAPI(base)
	compactor := m.Compactor
	if sc, ok := compactor.(*compaction.SimpleCompactor); ok {
		compactor = sc.WithConfig(cfg)
	}
	compact, report, err := compactor.Compact(ctx, m.sessionInfo(ctx, sess, originals), ctxMsgs, cfg.TargetTokens)
	report.CompactionGeneration = sess.CompactionGeneration
	if err != nil {
		return report, err
	}
	if !report.SessionCompacted && report.ChunksCompacted == 0 {
		return report, nil
	}
	if err := m.writeCompactionView(ctx, sess, originals, compact, report, coveredThroughOrd, coveredThroughID, sourceSeq); err != nil {
		return report, err
	}
	report.CompactionGeneration = sess.CompactionGeneration + 1
	return report, nil
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
	if prior, ok, err := m.store.GetCompactionView(ctx, sess.ID); err == nil && ok {
		current, err := m.store.CompactionViewCurrent(ctx, sess.ID, prior.CoveredThroughOrd, prior.CoveredThroughID, prior.SourceSeq)
		if err == nil && current {
			info.PriorContinuation = prior.Continuation
		}
	}
	if boundary := api.UserIntentBoundary(originals); boundary > 0 {
		request := originals[boundary-1] //nolint:gosec // UserIntentBoundary returns an index within originals.
		info.CurrentRequest = &request
		info.LatestUserRequestID = request.ID
	}
	return info
}

// model uses the same routed selection as the receiving coordinator.
func (m *Service) Model(ctx context.Context, sess *api.Session) (string, string) {
	if m == nil || sess == nil {
		return "", ""
	}
	if m.router != nil {
		var roots []string
		if m.Workspace != nil {
			roots = m.Workspace.SettingsRoots(ctx, sess)
		}
		selection, err := m.router.WithOverlayRoots(roots).ResolveSession(ctx, sess)
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
	history, err := m.Load(ctx, sess)
	if err != nil {
		return api.SessionContextResponse{}, err
	}
	_, report := m.Assemble(ctx, sess, history, "")
	est := m.BudgetTokens(ctx, sess, report.TokensAfter)
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

func (m *Service) compactionConfig(ctx context.Context, sess *api.Session) compaction.CompactionConfig {
	if m == nil {
		return compaction.DefaultCompactionConfig()
	}
	if m.Limits == nil || sess == nil {
		cfg := compaction.DefaultCompactionConfig()
		if m.Compactor != nil {
			cfg = m.Compactor.Config()
		}
		if sess != nil && sess.IsWorkerChild() {
			cfg = compaction.WorkerChildCompactionConfig(cfg)
		}
		return cfg
	}
	resolved := *sess
	resolved.ProviderID, resolved.Model = m.Model(ctx, sess)
	cfg := m.Limits.Compaction(ctx, &resolved)
	if sess.IsWorkerChild() {
		cfg = compaction.WorkerChildCompactionConfig(cfg)
	}
	return cfg
}
