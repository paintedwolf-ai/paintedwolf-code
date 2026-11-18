package history

import (
	"context"
	"errors"
	"log/slog"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type synthesisDiscard string

func (r synthesisDiscard) Error() string { return string(r) }

const (
	synthesisStale          synthesisDiscard = "stale"
	synthesisModelChanged   synthesisDiscard = "model_changed"
	synthesisNoContinuation synthesisDiscard = "no_continuation"
	synthesisNoAdvance      synthesisDiscard = "no_advance"
	synthesisNoSummaryPart  synthesisDiscard = "no_summary_part"
	synthesisWouldGrow      synthesisDiscard = "would_grow"
)

type synthesisSource struct {
	first, last     attemptSource
	through, from   int64
	rows            int
	provider, model string
}

// scheduleSynthesis starts optional enrichment for the published baseline.
func (m *Service) scheduleSynthesis(ctx context.Context, sc *compaction.SimpleCompactor, info compaction.SessionInfo, originals []api.Message, snapshot store.CompactionView) {
	if snapshot.Continuation == nil || len(originals) == 0 || ctx.Err() != nil {
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
		m.Runner.Synthesis.StartLatest(ctx, info.ID, func(workCtx context.Context) {
			m.runSynthesis(workCtx, token, sc, info, originals, snapshot)
		})
		return nil
	})
}

func (m *Service) runSynthesis(ctx context.Context, token lifecycle.Turn, sc *compaction.SimpleCompactor, info compaction.SessionInfo, originals []api.Message, snapshot store.CompactionView) {
	source := synthesisSource{
		first: attemptSource{ord: originals[0].Ord, id: originals[0].ID},
		last:  attemptSource{ord: originals[len(originals)-1].Ord, id: originals[len(originals)-1].ID},
		rows:  len(originals),
		from:  snapshot.Continuation.SummaryThroughOrd, through: snapshot.Continuation.ThroughOrd,
		provider: info.ProviderID, model: info.Model,
	}
	for _, row := range originals {
		source.last.seq = max(source.last.seq, row.Seq)
	}
	calls := 0
	var err error
	defer func() { logSynthesisOutcome(ctx, info.ID, snapshot.Generation, source, calls, err) }()
	beforeCall := func(callCtx context.Context) error {
		if err := m.checkSynthesisSnapshot(callCtx, info, snapshot); err != nil {
			return err
		}
		if calls == 0 {
			if prior, ok := m.synthesisAttempts.Load(info.ID); ok && prior == source {
				return synthesisNoAdvance
			}
			m.synthesisAttempts.Store(info.ID, source)
		}
		calls++
		return nil
	}
	var enriched *messageview.Continuation
	enriched, err = sc.EnrichContinuation(ctx, info, originals, snapshot.Continuation, beforeCall)
	if err != nil {
		return
	}
	err = m.Runner.Execute(ctx, info.ID, func(publishCtx context.Context) error {
		return m.Gate.WithSessionTreeAdmission(publishCtx, info.ID, func() error {
			if !m.Gate.MayDrain(token) {
				return synthesisStale
			}
			return m.publishSynthesis(publishCtx, info, snapshot, enriched)
		})
	})
}

func logSynthesisOutcome(ctx context.Context, id string, generation int, source synthesisSource, calls int, err error) {
	outcome, reason := "published", ""
	var diagnostic error
	var discard synthesisDiscard
	switch {
	case errors.As(err, &discard):
		outcome, reason = "discarded", string(discard)
	case errors.Is(err, context.Canceled):
		outcome, reason = "discarded", string(synthesisStale)
	case err != nil:
		outcome, reason, diagnostic = "discarded", "error", err
	}
	slog.InfoContext(ctx, "continuation synthesis settled", "session_id", id,
		"outcome", outcome, "reason", reason, "error", diagnostic,
		"generation", generation, "summary_after_ord", source.from,
		"through_ord", source.through, "source_first_ord", source.first.ord, "source_last_ord", source.last.ord, "model_calls", calls)
}

func (m *Service) checkSynthesisSnapshot(ctx context.Context, info compaction.SessionInfo, snapshot store.CompactionView) error {
	sess, err := m.store.Get(ctx, info.ID)
	if err != nil {
		return err
	}
	if sess.CompactionGeneration != snapshot.Generation {
		return synthesisStale
	}
	provider, model := m.Model(ctx, sess)
	if provider != info.ProviderID || model != info.Model {
		return synthesisModelChanged
	}
	current, err := m.store.CompactionViewCurrent(ctx, info.ID, snapshot.CoveredThroughOrd, snapshot.CoveredThroughID, snapshot.SourceSeq)
	if err == nil && !current {
		return synthesisStale
	}
	return err
}

func (m *Service) publishSynthesis(ctx context.Context, info compaction.SessionInfo, snapshot store.CompactionView, enriched *messageview.Continuation) error {
	if err := m.checkSynthesisSnapshot(ctx, info, snapshot); err != nil {
		return err
	}
	view, ok, err := m.store.GetCompactionView(ctx, info.ID)
	if err != nil {
		return err
	}
	if !ok || view.Continuation == nil {
		return synthesisNoContinuation
	}
	if view.Generation != snapshot.Generation {
		return synthesisStale
	}
	if view.Continuation.ThroughOrd != enriched.ThroughOrd || view.Continuation.SummaryThroughOrd >= enriched.SummaryThroughOrd {
		return synthesisNoAdvance
	}
	messages := append([]api.Message(nil), view.Messages...)
	for i, row := range messages {
		if len(row.ContentParts) != 1 || row.ContentParts[0].Source != "compaction_summary" {
			continue
		}
		return m.replaceContinuation(ctx, info, view, messages, i, enriched)
	}
	return synthesisNoSummaryPart
}

func (m *Service) replaceContinuation(ctx context.Context, info compaction.SessionInfo, view *store.CompactionView, messages []api.Message, index int, enriched *messageview.Continuation) error {
	row := messages[index]
	budget := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI([]api.Message{row}))
	enriched.RenderTokens = max(1, budget)
	text, err := compaction.RenderContinuation(ctx, enriched, info.RecallAvailable, 0)
	if err != nil {
		return err
	}
	row.Content = text
	row.ContentParts = append([]api.MessageContentPart(nil), row.ContentParts...)
	row.ContentParts[0].Content = text
	messages[index] = row
	fits, err := compaction.ProjectionDoesNotGrow(info, view.Messages, messages)
	if err != nil {
		return err
	}
	if !fits || enriched.Level == "references" {
		return synthesisWouldGrow
	}
	view.Messages = messages
	view.Continuation = enriched
	view.Generation++
	view.TokensAfter = compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(messages))
	return m.store.PutCompactionView(ctx, info.ID, *view)
}
