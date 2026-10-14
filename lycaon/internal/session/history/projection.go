package history

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/pkg/api"
)

// Load uses a bounded tail when a projection cannot cover the live suffix.
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
		return m.loadBoundedCanonicalPromptHistory(ctx, sess)
	}
	return suffix, nil
}

func (m *Service) loadBoundedCanonicalPromptHistory(ctx context.Context, sess *api.Session) ([]api.Message, error) {
	history, err := m.store.GetMessagesTail(ctx, sess.ID, UncompactedLimit)
	if err != nil {
		return nil, err
	}
	if len(history) == 0 || history[0].Ord <= 1 {
		return history, nil
	}
	firstTailOrd := history[0].Ord
	history, err = m.withCurrentRequest(ctx, sess, history)
	if err != nil {
		return nil, err
	}
	m.Schedule(ctx, sess)
	text, err := guidance.RenderGuidance(ctx, guidance.CompactionContinuationRecordRef, map[string]any{
		"through_ord": firstTailOrd - 1, "reference_only": true,
		"recall_available": m.recallAvailable != nil && m.recallAvailable(ctx, sess),
	})
	if err != nil {
		return nil, err
	}
	notice := api.Message{ID: "host-history-coverage", Role: api.MessageRoleSystem, Origin: api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted, ContextPinned: true, Content: text}
	return append([]api.Message{notice}, history...), nil
}

// Fit assembles prompt history and schedules durable compaction.
func (m *Service) Fit(ctx context.Context, sess *api.Session, history []api.Message, surfaceID string) ([]api.Message, compaction.CompactionReport, error) {
	assembled, report := m.Assemble(ctx, sess, history, surfaceID)
	if m.Compactor != nil {
		cfg := m.compactionConfig(ctx, sess)
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

// withCurrentRequest restores the active human opener when it predates the tail.
func (m *Service) withCurrentRequest(ctx context.Context, sess *api.Session, tail []api.Message) ([]api.Message, error) {
	if sess.CurrentTurn <= 0 || len(tail) == 0 || int64(sess.CurrentTurn) >= tail[0].Ord {
		return tail, nil
	}
	rows, err := m.store.GetMessagesAfterOrd(ctx, sess.ID, int64(sess.CurrentTurn)-1, 1)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || rows[0].Ord != int64(sess.CurrentTurn) {
		return tail, nil
	}
	return append(rows, tail...), nil
}
