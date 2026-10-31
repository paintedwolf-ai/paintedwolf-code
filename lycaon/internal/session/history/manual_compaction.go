package history

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptassembly"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// ForceCompact publishes deterministic reductions and schedules optional synthesis.
func (m *Service) ForceCompact(ctx context.Context, sessionID string) (report compaction.CompactionReport, err error) {
	turn, err := m.Gate.Capture(ctx, sessionID)
	if err != nil {
		return report, err
	}
	err = m.Runner.Execute(ctx, sessionID, func(runCtx context.Context) error {
		// Register cancellation before releasing stop admission.
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
	info := m.sessionInfo(ctx, sess, originals)
	compactor := m.Compactor
	var synth *compaction.SimpleCompactor
	if sc, ok := compactor.(*compaction.SimpleCompactor); ok {
		synth = sc.WithConfig(cfg)
		compactor = synth.Baseline()
	}
	baselineInfo := info
	if synth != nil {
		baselineInfo.Attempts = nil
	}
	compact, report, err := compactor.Compact(ctx, baselineInfo, ctxMsgs, cfg.TargetTokens)
	report.CompactionGeneration = sess.CompactionGeneration
	if err != nil {
		return report, err
	}
	if !report.SessionCompacted && report.ChunksCompacted == 0 {
		if synth != nil {
			m.scheduleProjectionSynthesis(ctx, synth, info, originals, ctxMsgs, report, store.CompactionView{Generation: sess.CompactionGeneration,
				CoveredThroughOrd: coveredThroughOrd, CoveredThroughID: coveredThroughID, SourceSeq: sourceSeq})
		}
		return report, nil
	}
	if err := m.writeCompactionView(ctx, sess, originals, compact, report, coveredThroughOrd, coveredThroughID, sourceSeq); err != nil {
		return report, err
	}
	report.CompactionGeneration = sess.CompactionGeneration + 1
	if synth != nil {
		m.scheduleProjectionSynthesis(ctx, synth, info, originals, compact, report, store.CompactionView{Generation: report.CompactionGeneration, Continuation: report.Continuation,
			CoveredThroughOrd: coveredThroughOrd, CoveredThroughID: coveredThroughID, SourceSeq: sourceSeq})
	}
	return report, nil
}
