package history

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
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
	if !ok || view.Generation < sess.CompactionGeneration {
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
	// Background publication may advance the caller's session snapshot.
	sess.CompactionGeneration = view.Generation
	cfg := m.compactionConfig(ctx, sess)
	if restored, ok, err := m.restoreCanonicalTail(ctx, sess, view, cfg); err != nil {
		return nil, err
	} else if ok {
		return restored, nil
	}
	bounds := m.canonicalTailBounds(cfg)
	suffix, hasMore, err := m.readBoundedSuffix(ctx, sess.ID, view.CoveredThroughOrd, bounds)
	if err != nil {
		return nil, err
	}
	if hasMore {
		return m.loadBoundedCanonicalPromptHistory(ctx, sess)
	}
	active, err := m.loadActiveWorkingSet(ctx, sess, cfg, view.CoveredThroughOrd)
	if err != nil {
		return nil, err
	}
	activeOrd := int64(0)
	if len(active) > 0 {
		activeOrd = active[0].Ord
		suffix = active
	}
	// Replay fields stay canonical rather than being duplicated in projections.
	retained := make([]api.Message, 0, len(view.Messages)+len(suffix))
	for _, row := range view.Messages {
		if row.Ord <= 0 || row.Role != api.MessageRoleAssistant || row.CompactedChunk != nil || activeOrd > 0 && row.Ord >= activeOrd {
			continue
		}
		canonical, err := m.store.GetMessage(ctx, sess.ID, row.ID)
		if err != nil {
			return nil, err
		}
		retained = append(retained, canonical)
	}
	return append(retained, suffix...), nil
}

func (m *Service) loadBoundedCanonicalPromptHistory(ctx context.Context, sess *api.Session) ([]api.Message, error) {
	bounds := m.canonicalTailBounds(m.compactionConfig(ctx, sess))
	history, err := m.readBoundedCanonicalTail(ctx, sess.ID, bounds)
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
		"level": "references", "through_ord": firstTailOrd - 1, "reference_only": true,
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
		est := m.BudgetTokens(ctx, sess, report.TokensBefore)
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
	if err != nil || !ok || view.Generation < sess.CompactionGeneration {
		return history
	}
	current, currentErr := m.store.CompactionViewCurrent(ctx, sess.ID, view.CoveredThroughOrd, view.CoveredThroughID, view.SourceSeq)
	if currentErr != nil || !current {
		return history
	}
	sess.CompactionGeneration = view.Generation
	cfg := m.compactionConfig(ctx, sess)
	if restored, ok := m.applyRestoredReceiverView(ctx, sess, view, history, cfg); ok {
		return restored
	}
	continuationText := ""
	if view.Continuation != nil {
		record := messageview.CloneContinuation(view.Continuation)
		record.Window = cfg.ModelContextWindow
		record.Receiver = sess.Model
		recall := m.recallAvailable != nil && m.recallAvailable(ctx, sess)
		text, renderErr := compaction.RenderContinuation(ctx, record, recall, 0)
		if renderErr != nil {
			return history
		}
		continuationText = text
	}
	byID := make(map[string]api.Message, len(history))
	for _, msg := range history {
		byID[msg.ID] = msg
	}
	viewByID := make(map[string]api.Message, len(view.Messages))
	for _, msg := range view.Messages {
		if msg.ID != "" {
			viewByID[msg.ID] = msg
		}
	}
	intent, _ := api.LastUserIntentMessage(history)
	activeOrd := intent.Ord
	out := make([]api.Message, 0, len(view.Messages)+8)
	for _, msg := range view.Messages {
		if activeOrd > 0 && msg.Ord >= activeOrd {
			continue
		}
		if canonical, ok := byID[msg.ID]; ok {
			// Canonical rows restore fields excluded from prompt projections.
			msg = api.RehydrateTranscriptProjectionFields(msg, canonical)
		}
		if continuationText != "" && len(msg.ContentParts) == 1 && msg.ContentParts[0].Source == "compaction_summary" {
			msg.Content = continuationText
			msg.ContentParts = append([]api.MessageContentPart(nil), msg.ContentParts...)
			msg.ContentParts[0].Content = continuationText
		}
		out = append(out, msg)
	}
	for _, msg := range history {
		if msg.Ord <= view.CoveredThroughOrd && (activeOrd <= 0 || msg.Ord < activeOrd) {
			continue
		}
		if viewMsg, ok := viewByID[msg.ID]; ok && viewMsg.CompactedChunk != nil {
			msg = api.RehydrateTranscriptProjectionFields(viewMsg, msg)
		}
		out = append(out, msg)
	}
	return out
}

// withCurrentRequest restores the active human opener when it predates the tail.
func (m *Service) withCurrentRequest(ctx context.Context, sess *api.Session, tail []api.Message) ([]api.Message, error) {
	if len(tail) == 0 {
		return tail, nil
	}
	currentTurn := sess.CurrentTurn
	if currentTurn <= 0 {
		if latest, err := m.store.Get(ctx, sess.ID); err == nil && latest != nil {
			currentTurn = latest.CurrentTurn
		}
	}
	if currentTurn <= 0 {
		return tail, nil
	}
	turn, err := m.store.ReadSourceTurn(ctx, sess.ID, currentTurn)
	if err != nil {
		return nil, err
	}
	if turn.OpeningMessageID == "" {
		return tail, nil
	}
	opener, err := m.store.GetMessage(ctx, sess.ID, turn.OpeningMessageID)
	if err != nil {
		return nil, err
	}
	if opener.ID == "" || opener.Ord >= tail[0].Ord {
		return tail, nil
	}
	return append([]api.Message{opener}, tail...), nil
}
