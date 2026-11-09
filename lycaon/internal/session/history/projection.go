package history

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

const historyCoverageNoticeID = "host-history-coverage"

// Load uses a bounded tail when a projection cannot cover the live suffix.
func (m *Service) Load(ctx context.Context, sess *api.Session) ([]api.Message, error) {
	return m.project(ctx, sess, true)
}

// Project reads history without scheduling compaction or changing the supplied session.
func (m *Service) Project(ctx context.Context, sess *api.Session) ([]api.Message, error) {
	if sess != nil {
		copy := *sess
		sess = &copy
	}
	rows, err := m.project(ctx, sess, false)
	if err != nil {
		return nil, err
	}
	return m.ApplyView(ctx, sess, rows), nil
}

func (m *Service) project(ctx context.Context, sess *api.Session, refresh bool) ([]api.Message, error) {
	if m == nil || m.store == nil || sess == nil {
		return nil, fmt.Errorf("session store and session are required")
	}
	view, ok, err := m.store.GetCompactionView(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	if !ok || view.Generation < sess.CompactionGeneration {
		return m.loadBoundedCanonicalPromptHistory(ctx, sess, refresh)
	}
	current, err := m.store.CompactionViewCurrent(ctx, sess.ID, view.CoveredThroughOrd, view.CoveredThroughID, view.SourceSeq)
	if err != nil {
		return nil, err
	}
	if !current {
		if refresh {
			m.Schedule(ctx, sess)
		}
		return m.loadBoundedCanonicalPromptHistory(ctx, sess, refresh)
	}
	// Background publication may advance the caller's session snapshot.
	sess.CompactionGeneration = view.Generation
	cfg := m.compactionConfig(ctx, sess)
	if restored, ok, err := m.restoreCanonicalTail(ctx, sess, view, cfg); err != nil {
		return nil, err
	} else if ok {
		return restored, nil
	}
	return m.loadViewHistory(ctx, sess, view, cfg, refresh)
}

// loadViewHistory reads the canonical rows a valid view does not replace, plus
// the canonical copies of its retained assistant rows for replay fields. Those
// copies sit before the opener: the rows after it are the contiguous working tail.
func (m *Service) loadViewHistory(ctx context.Context, sess *api.Session, view *store.CompactionView, cfg compaction.CompactionConfig, refresh bool) ([]api.Message, error) {
	covered := view.CoveredThroughOrd
	bounds := canonicalTailBounds(cfg)
	tail, truncated, err := m.readCanonicalTail(ctx, sess.ID, bounds, viewTailStop(covered, activeTurnAllocation(cfg)))
	if err != nil {
		return nil, err
	}
	tail, truncated = m.cutoffs.holdTail(m.cutoffKey(ctx, sess, cfg, view), sess.ID, tail, truncated, bounds)
	oldest := int64(0)
	if len(tail) > 0 {
		oldest = tail[0].Ord
	}
	if _, ok := api.LastUserIntentMessage(tail); !ok {
		if tail, err = withCurrentRequest(ctx, m.store, sess, tail); err != nil {
			return nil, err
		}
	}
	splice := planViewSplice(tail, view, m.cutoffs.holdWorkingSet(m.cutoffKey(ctx, sess, cfg, view), sess.ID, tail, activeTurnAllocation(cfg)))
	viewBounds := indexDraftExchanges(view.Messages)
	var out []api.Message
	for _, row := range view.Messages {
		if row.Ord <= 0 || row.Role != api.MessageRoleAssistant || row.CompactedChunk != nil || viewBounds.maxOrdFor(row) > splice.replacedThrough {
			continue
		}
		canonical, err := m.store.GetMessage(ctx, sess.ID, row.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, canonical)
	}
	if truncated {
		notice, err := tailOmissionNotice(ctx, m.recallAvailable, sess, covered, oldest, tail)
		if err != nil {
			return nil, err
		}
		if notice != nil {
			if refresh {
				m.Schedule(ctx, sess)
			}
			out = append(out, *notice)
		}
	}
	return append(out, tail...), nil
}

func (m *Service) loadBoundedCanonicalPromptHistory(ctx context.Context, sess *api.Session, refresh bool) ([]api.Message, error) {
	cfg := m.compactionConfig(ctx, sess)
	bounds := canonicalTailBounds(cfg)
	history, truncated, err := m.readCanonicalTail(ctx, sess.ID, bounds, nil)
	if err != nil {
		return nil, err
	}
	history, _ = m.cutoffs.holdTail(m.cutoffKey(ctx, sess, cfg, nil), sess.ID, history, truncated, bounds)
	if len(history) == 0 || history[0].Ord <= 1 {
		return history, nil
	}
	firstTailOrd := history[0].Ord
	history, err = withCurrentRequest(ctx, m.store, sess, history)
	if err != nil {
		return nil, err
	}
	if refresh {
		m.Schedule(ctx, sess)
	}
	notice, err := tailOmissionNotice(ctx, m.recallAvailable, sess, 0, firstTailOrd, history)
	if err != nil || notice == nil {
		return history, err
	}
	return append([]api.Message{*notice}, history...), nil
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
	splice := planViewSplice(history, view, m.cutoffs.holdWorkingSet(m.cutoffKey(ctx, sess, cfg, view), sess.ID, history, activeTurnAllocation(cfg)))
	bounds := indexDraftExchanges(history)
	viewBounds := indexDraftExchanges(view.Messages)
	var head, older, within []api.Message
	for _, msg := range view.Messages {
		if viewBounds.maxOrdFor(msg) > splice.replacedThrough || splice.intentIdx >= 0 && msg.Ord == splice.intentOrd {
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
		switch {
		case msg.Ord <= 0:
			head = append(head, msg)
		case splice.intentIdx < 0 || msg.Ord < splice.intentOrd:
			older = append(older, msg)
		default:
			within = append(within, msg)
		}
	}
	var beforeIntent, tail, notice []api.Message
	for i, msg := range history {
		if msg.ID == historyCoverageNoticeID {
			notice = append(notice[:0], msg)
			continue
		}
		if i == splice.intentIdx || isCompactionHeadRow(msg, viewByID) {
			continue
		}
		if msg.Ord > 0 && bounds.maxOrdFor(msg) <= splice.replacedThrough {
			continue
		}
		if viewMsg, ok := viewByID[msg.ID]; ok && viewMsg.CompactedChunk != nil {
			msg = api.RehydrateTranscriptProjectionFields(viewMsg, msg)
		}
		if splice.intentIdx >= 0 && (msg.Ord > 0 && msg.Ord < splice.intentOrd || msg.Ord <= 0 && i < splice.intentIdx) {
			beforeIntent = append(beforeIntent, msg)
		} else {
			tail = append(tail, msg)
		}
	}
	out := make([]api.Message, 0, len(view.Messages)+len(history)+1)
	if splice.openerFirst {
		out = append(append(append(out, older...), beforeIntent...), history[splice.intentIdx])
		out = append(out, head...)
	} else {
		out = append(append(append(out, head...), older...), beforeIntent...)
		if splice.intentIdx >= 0 {
			out = append(out, history[splice.intentIdx])
		}
	}
	return api.FilterPromptHistory(append(append(append(out, within...), notice...), tail...))
}

// withCurrentRequest restores the active human opener when it predates the tail.
func withCurrentRequest(ctx context.Context, store Store, sess *api.Session, tail []api.Message) ([]api.Message, error) {
	currentTurn := sess.CurrentTurn
	if currentTurn <= 0 {
		if latest, err := store.Get(ctx, sess.ID); err == nil && latest != nil {
			currentTurn = latest.CurrentTurn
		}
	}
	if currentTurn <= 0 {
		return tail, nil
	}
	turn, err := store.ReadSourceTurn(ctx, sess.ID, currentTurn)
	if err != nil {
		return nil, err
	}
	if turn.OpeningMessageID == "" {
		return tail, nil
	}
	opener, err := store.GetMessage(ctx, sess.ID, turn.OpeningMessageID)
	if err != nil {
		return nil, err
	}
	if opener.ID == "" || !api.IsUserIntentMessage(opener) {
		return tail, nil
	}
	return retainRequestIntent(tail, []api.Message{opener}), nil
}
