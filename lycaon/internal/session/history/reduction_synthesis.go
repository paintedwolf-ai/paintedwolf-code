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
		m.scheduleSynthesis(ctx, sc, info, originals, snapshot)
	} else if report.Reason == "insufficient_savings" {
		m.scheduleReductionSynthesis(ctx, sc, info, originals, prepared, snapshot)
	}
}

// scheduleReductionSynthesis prepares a useful page without requiring a published baseline.
func (m *Service) scheduleReductionSynthesis(ctx context.Context, sc *compaction.SimpleCompactor, info compaction.SessionInfo, originals []api.Message, prepared []compaction.ContextMessage, snapshot store.CompactionView) {
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
			candidate, report, err := sc.Compact(workCtx, info, prepared, sc.Config().TargetTokens)
			if err != nil || workCtx.Err() != nil || !report.SessionCompacted {
				return
			}
			_ = m.Runner.Execute(workCtx, info.ID, func(publishCtx context.Context) error {
				return m.Gate.WithSessionTreeAdmission(publishCtx, info.ID, func() error {
					if !m.Gate.MayDrain(token) {
						return lifecycle.ErrStopping
					}
					return m.publishReductionSynthesis(publishCtx, info, originals, snapshot, candidate, report)
				})
			})
		})
		return nil
	})
}

func (m *Service) publishReductionSynthesis(ctx context.Context, info compaction.SessionInfo, originals []api.Message, snapshot store.CompactionView, candidate []compaction.ContextMessage, report compaction.CompactionReport) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !report.SessionCompacted || report.TokensAfter >= report.TokensBefore {
		return nil
	}
	current, err := m.store.CompactionViewCurrent(ctx, info.ID, snapshot.CoveredThroughOrd, snapshot.CoveredThroughID, snapshot.SourceSeq)
	if err != nil || !current {
		return err
	}
	sess, err := m.store.Get(ctx, info.ID)
	if err != nil || sess == nil || sess.CompactionGeneration != snapshot.Generation {
		return err
	}
	provider, model := m.Model(ctx, sess)
	if provider != info.ProviderID || model != info.Model {
		return nil
	}
	if err := m.writeCompactionView(ctx, sess, originals, candidate, report, snapshot.CoveredThroughOrd, snapshot.CoveredThroughID, snapshot.SourceSeq); err != nil {
		return err
	}
	m.Schedule(ctx, sess)
	return nil
}
