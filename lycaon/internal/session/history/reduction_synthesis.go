package history

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) scheduleProjectionSynthesis(ctx context.Context, sc *compaction.SimpleCompactor, info compaction.SessionInfo, originals []api.Message, prepared []compaction.ContextMessage, report compaction.CompactionReport, snapshot store.CompactionView) {
	if report.SessionCompacted {
		m.scheduleSynthesis(ctx, sc, info, compaction.ContextMessagesToAPI(report.SynthesisSources, originals), snapshot)
	} else if report.Reason == "insufficient_savings" {
		m.scheduleReductionSynthesis(ctx, sc, info, originals, prepared, snapshot, report.TargetTokens)
	}
}

// scheduleReductionSynthesis prepares a useful page without requiring a published baseline.
func (m *Service) scheduleReductionSynthesis(ctx context.Context, sc *compaction.SimpleCompactor, info compaction.SessionInfo, originals []api.Message, prepared []compaction.ContextMessage, snapshot store.CompactionView, target int) {
	if ctx.Err() != nil {
		return
	}
	token, err := m.Gate.Capture(ctx, info.ID)
	if err != nil {
		return
	}
	_ = m.Gate.WithSessionTreeAdmission(ctx, info.ID, func() error {
		if !m.Gate.MayDrain(token) {
			return lifecycle.ErrStopping
		}
		m.Runner.Synthesis.Start(ctx, info.ID, func(workCtx context.Context) {
			candidate, report, err := sc.Compact(workCtx, info, prepared, target)
			if err != nil || workCtx.Err() != nil || !report.SessionCompacted && report.ChunksCompacted == 0 {
				return
			}
			published := false
			err = m.Runner.Execute(workCtx, info.ID, func(publishCtx context.Context) error {
				return m.Gate.WithSessionTreeAdmission(publishCtx, info.ID, func() error {
					if !m.Gate.MayDrain(token) {
						return lifecycle.ErrStopping
					}
					var publishErr error
					published, publishErr = m.publishReductionSynthesis(publishCtx, info, originals, snapshot, candidate, report)
					return publishErr
				})
			})
			if err == nil && published {
				if sess, err := m.store.Get(workCtx, info.ID); err == nil {
					m.Schedule(workCtx, sess)
				}
			}
		})
		return nil
	})
}

func (m *Service) publishReductionSynthesis(ctx context.Context, info compaction.SessionInfo, originals []api.Message, snapshot store.CompactionView, candidate []compaction.ContextMessage, report compaction.CompactionReport) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !report.SessionCompacted && report.ChunksCompacted == 0 || report.TokensAfter >= report.TokensBefore {
		return false, nil
	}
	current, err := m.store.CompactionViewCurrent(ctx, info.ID, snapshot.CoveredThroughOrd, snapshot.CoveredThroughID, snapshot.SourceSeq)
	if err != nil || !current {
		return false, err
	}
	sess, err := m.store.Get(ctx, info.ID)
	if err != nil || sess == nil || sess.CompactionGeneration != snapshot.Generation {
		return false, err
	}
	provider, model := m.Model(ctx, sess)
	if provider != info.ProviderID || model != info.Model {
		return false, nil
	}
	if err := m.writeCompactionView(ctx, sess, originals, candidate, report, snapshot.CoveredThroughOrd, snapshot.CoveredThroughID, snapshot.SourceSeq); err != nil {
		return false, err
	}
	return true, nil
}
