package history

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// scheduleSynthesis enriches a published baseline using its captured source identity.
func (m *Service) scheduleSynthesis(ctx context.Context, sc *compaction.SimpleCompactor, info compaction.SessionInfo, originals []api.Message, snapshot store.CompactionView) {
	if snapshot.Continuation == nil || len(snapshot.Continuation.Sources) == 0 {
		return
	}
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
			enriched, err := sc.EnrichContinuation(workCtx, info, originals, snapshot.Continuation)
			if err != nil || workCtx.Err() != nil {
				return
			}
			_ = m.Runner.Execute(workCtx, info.ID, func(publishCtx context.Context) error {
				return m.Gate.WithSessionTreeAdmission(publishCtx, info.ID, func() error {
					if !m.Gate.MayDrain(token) {
						return lifecycle.ErrStopping
					}
					return m.publishSynthesis(publishCtx, info, snapshot, enriched)
				})
			})
		})
		return nil
	})
}

func (m *Service) publishSynthesis(ctx context.Context, info compaction.SessionInfo, snapshot store.CompactionView, enriched *messageview.Continuation) error {
	current, err := m.store.CompactionViewCurrent(ctx, info.ID, snapshot.CoveredThroughOrd, snapshot.CoveredThroughID, snapshot.SourceSeq)
	if err != nil || !current {
		return err
	}
	sess, err := m.store.Get(ctx, info.ID)
	if err != nil {
		return err
	}
	provider, model := m.Model(ctx, sess)
	if provider != info.ProviderID || model != info.Model {
		return nil
	}
	view, ok, err := m.store.GetCompactionView(ctx, info.ID)
	if err != nil || !ok || view.Continuation == nil {
		return err
	}
	// Newer projections retain their own coverage and recovery coordinates.
	if view.Continuation.ThroughOrd < enriched.ThroughOrd ||
		view.Continuation.SummaryThroughOrd >= enriched.SummaryThroughOrd {
		return nil
	}
	accepted := *view.Continuation
	if accepted.ThroughOrd == enriched.ThroughOrd {
		accepted.Sources = messageview.CloneContinuation(enriched).Sources
	}
	accepted.Summary = enriched.Summary
	accepted.SummaryThroughOrd = enriched.SummaryThroughOrd
	accepted.Reason = enriched.Reason
	if accepted.SummaryThroughOrd < accepted.ThroughOrd {
		accepted.Reason = "references_only"
	}
	text, err := compaction.RenderContinuation(ctx, &accepted, info.RecallAvailable)
	if err != nil {
		return err
	}
	messages := append([]api.Message(nil), view.Messages...)
	replaced := false
	for i, row := range messages {
		if len(row.ContentParts) != 1 || row.ContentParts[0].Source != "compaction_summary" {
			continue
		}
		row.Content = text
		row.ContentParts = append([]api.MessageContentPart(nil), row.ContentParts...)
		row.ContentParts[0].Content = text
		messages[i] = row
		replaced = true
	}
	if !replaced {
		return nil
	}
	view.Messages = messages
	view.Continuation = &accepted
	view.Generation = sess.CompactionGeneration + 1
	view.TokensAfter = compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(messages))
	return m.store.PutCompactionView(ctx, info.ID, *view)
}
