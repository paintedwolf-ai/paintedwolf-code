package history

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) scheduleProjectionSynthesis(ctx context.Context, sc *compaction.SimpleCompactor, info compaction.SessionInfo, originals []api.Message, prepared []compaction.ContextMessage, report compaction.CompactionReport, snapshot store.CompactionView, attempt attemptPass) {
	if report.SessionCompacted {
		m.scheduleSynthesis(ctx, sc, info, compaction.ContextMessagesToAPI(report.SynthesisSources, originals), snapshot)
	} else if report.Reason == "insufficient_savings" {
		m.scheduleReductionSynthesis(ctx, sc, info, reductionInput{originals: originals, prepared: prepared, snapshot: snapshot, target: report.TargetTokens}, attempt)
	}
}

// reductionInput is the captured page one synthesis evaluates and may publish.
type reductionInput struct {
	originals []api.Message
	prepared  []compaction.ContextMessage
	snapshot  store.CompactionView
	target    int
}

// scheduleReductionSynthesis prepares a useful page without requiring a published baseline.
func (m *Service) scheduleReductionSynthesis(ctx context.Context, sc *compaction.SimpleCompactor, info compaction.SessionInfo, input reductionInput, attempt attemptPass) {
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
		started := m.Runner.Synthesis.Start(ctx, info.ID, func(workCtx context.Context) {
			m.runReductionSynthesis(workCtx, token, sc, info, input, attempt)
		})
		if !started {
			m.attemptControl().deferRetry(info.ID, attempt)
		}
		return nil
	})
}

// runReductionSynthesis settles the outcome under the admission that fences publication.
func (m *Service) runReductionSynthesis(ctx context.Context, token lifecycle.Turn, sc *compaction.SimpleCompactor, info compaction.SessionInfo, input reductionInput, attempt attemptPass) {
	memo := &pendingAttemptMemo{CompactionAttemptStore: m.store}
	info.Attempts = memo
	if attempt.explicit {
		info.Attempts = nil
	}
	candidate, report, compactErr := sc.Compact(ctx, info, input.prepared, input.target)
	if ctx.Err() != nil {
		return
	}
	published := false
	err := m.Runner.Execute(ctx, info.ID, func(publishCtx context.Context) error {
		return m.Gate.WithSessionTreeAdmission(publishCtx, info.ID, func() error {
			if !m.Gate.MayDrain(token) {
				return lifecycle.ErrStopping
			}
			if compactErr == nil && (report.SessionCompacted || report.ChunksCompacted > 0) {
				var publishErr error
				published, publishErr = m.publishReductionSynthesis(publishCtx, info, input.originals, input.snapshot, candidate, report)
				if publishErr != nil {
					return publishErr
				}
			}
			if !published {
				m.attemptControl().settleSynthesis(publishCtx, sc, info, attempt, report, compactErr, input.snapshot.CoveredThroughOrd, memo.pending)
			}
			return nil
		})
	})
	if err == nil && published {
		if sess, err := m.store.Get(ctx, info.ID); err == nil {
			m.Schedule(ctx, sess)
		}
	}
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
