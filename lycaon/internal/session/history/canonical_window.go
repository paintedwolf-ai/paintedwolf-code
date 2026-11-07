package history

import (
	"context"
	"encoding/json"
	"math"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type tailWindowBounds struct {
	maxRows   int
	maxTokens int
	maxBytes  int
}

func canonicalTailBounds(cfg compaction.CompactionConfig) tailWindowBounds {
	maxRows := min(messageview.MaxCompactionRows, max(UncompactedLimit, cfg.ModelContextWindow/512))
	maxTokens := max(cfg.TargetTokens, cfg.HardCeilingTokens)
	if maxTokens <= 0 {
		maxTokens = 65536
	}
	maxBytes := min(4<<20, max(64<<10, maxTokens*4))
	return tailWindowBounds{maxRows: maxRows, maxTokens: maxTokens, maxBytes: maxBytes}
}

func (m *Service) readBoundedCanonicalTail(ctx context.Context, sessionID string, bounds tailWindowBounds) ([]api.Message, error) {
	tail, _, err := m.readCanonicalTail(ctx, sessionID, bounds, nil)
	return tail, err
}

// readCanonicalTail reads backward through complete exchanges until stop or a
// hard bound; truncated reports that a hard bound ended the read. The latest
// exchange is admitted whole, and a bound never strands a result from its call.
func (m *Service) readCanonicalTail(ctx context.Context, sessionID string, bounds tailWindowBounds, stop func(api.Message) bool) ([]api.Message, bool, error) {
	if bounds.maxRows <= 0 {
		bounds.maxRows = UncompactedLimit
	}
	var pages [][]api.Message
	var beforeOrd int64
	var rowsCount, usedTokens, usedBytes int
	needExchange := true
	stopped, truncated := false, false
	for !stopped && !truncated {
		page, err := m.store.ReadMessageWindow(ctx, sessionID, messageview.MessageWindow{
			BeforeOrd: beforeOrd,
			Limit:     UncompactedLimit,
		})
		if err != nil {
			return nil, false, err
		}
		if len(page) == 0 {
			break
		}
		admittedStart := len(page)
		for i := len(page) - 1; i >= 0; i-- {
			msgTokens := estimateSingleMessageTokens(page[i])
			msgBytes := estimateSingleMessageBytes(page[i])
			halted := stop != nil && stop(page[i])
			if !needExchange {
				if halted {
					stopped = true
					break
				}
				if rowsCount >= bounds.maxRows || (bounds.maxTokens > 0 && usedTokens+msgTokens > bounds.maxTokens) || (bounds.maxBytes > 0 && usedBytes+msgBytes > bounds.maxBytes) {
					truncated = true
					break
				}
			}
			admittedStart = i
			needExchange = page[i].Role == api.MessageRoleTool
			rowsCount++
			usedTokens += msgTokens
			usedBytes += msgBytes
		}
		if admittedStart < len(page) {
			pages = append(pages, page[admittedStart:])
		}
		if len(page) < UncompactedLimit || page[0].Ord <= 1 {
			break
		}
		beforeOrd = page[0].Ord
	}
	out := make([]api.Message, 0, rowsCount)
	for i := len(pages) - 1; i >= 0; i-- {
		out = append(out, pages[i]...)
	}
	if len(out) > 0 && out[0].Ord > 1 {
		out = out[completeExchangeStart(out, 0):]
	}
	return out, truncated, nil
}

func (m *Service) readBoundedSuffix(ctx context.Context, sessionID string, afterOrd int64, bounds tailWindowBounds) ([]api.Message, bool, error) {
	if bounds.maxRows <= 0 {
		bounds.maxRows = UncompactedLimit
	}
	var out []api.Message
	var usedTokens, usedBytes int
	cursor := afterOrd
	for len(out) < bounds.maxRows {
		remaining := bounds.maxRows - len(out)
		pageLimit := min(UncompactedLimit, remaining) + 1
		page, err := m.store.ReadMessageWindow(ctx, sessionID, messageview.MessageWindow{
			After:    true,
			AfterOrd: cursor,
			Limit:    pageLimit,
		})
		if err != nil {
			return nil, false, err
		}
		if len(page) == 0 {
			return out, false, nil
		}
		for _, msg := range page {
			if len(out) >= bounds.maxRows {
				return out, true, nil
			}
			msgTokens := estimateSingleMessageTokens(msg)
			msgBytes := estimateSingleMessageBytes(msg)
			if len(out) >= UncompactedLimit && ((bounds.maxTokens > 0 && usedTokens+msgTokens > bounds.maxTokens) || (bounds.maxBytes > 0 && usedBytes+msgBytes > bounds.maxBytes)) {
				return out, true, nil
			}
			out = append(out, msg)
			usedTokens += msgTokens
			usedBytes += msgBytes
			cursor = msg.Ord
		}
		if len(page) < pageLimit {
			return out, false, nil
		}
	}
	return out, false, nil
}

func (m *Service) restoreCanonicalTail(ctx context.Context, sess *api.Session, view *store.CompactionView, cfg compaction.CompactionConfig) ([]api.Message, bool, error) {
	if view == nil || view.Continuation == nil || view.Continuation.Window <= 0 || cfg.ModelContextWindow <= view.Continuation.Window || cfg.TargetTokens <= 0 {
		return nil, false, nil
	}
	bounds := canonicalTailBounds(cfg)
	bounds.maxTokens = cfg.TargetTokens
	bounds.maxBytes = min(4<<20, max(64<<10, cfg.TargetTokens*4))
	tail, err := m.readBoundedCanonicalTail(ctx, sess.ID, bounds)
	if err != nil || len(tail) == 0 {
		return nil, false, err
	}
	if tail[0].Ord <= 1 {
		return tail, true, nil
	}
	if tail[0].Ord > view.Continuation.ThroughOrd {
		return nil, false, nil
	}
	tail, err = withCurrentRequest(ctx, m.store, sess, tail)
	if err != nil {
		return nil, false, err
	}
	return tail, true, nil
}

func (m *Service) applyRestoredReceiverView(ctx context.Context, sess *api.Session, view *store.CompactionView, history []api.Message, cfg compaction.CompactionConfig) ([]api.Message, bool) {
	if view == nil || view.Continuation == nil || view.Continuation.Window <= 0 || cfg.ModelContextWindow <= view.Continuation.Window || cfg.TargetTokens <= 0 {
		return nil, false
	}
	bounds := canonicalTailBounds(cfg)
	bounds.maxTokens = cfg.TargetTokens
	bounds.maxBytes = min(4<<20, max(64<<10, cfg.TargetTokens*4))
	tail, err := m.readBoundedCanonicalTail(ctx, sess.ID, bounds)
	if err != nil || len(tail) == 0 {
		return nil, false
	}
	byID := make(map[string]api.Message, len(history))
	for _, msg := range history {
		byID[msg.ID] = msg
	}
	for i := range tail {
		if canonical, ok := byID[tail[i].ID]; ok {
			tail[i] = api.RehydrateTranscriptProjectionFields(tail[i], canonical)
		}
	}
	if tail[0].Ord <= 1 && compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(tail)) <= cfg.TargetTokens {
		return tail, true
	}
	reserve := min(2048, max(256, cfg.TargetTokens/8))
	budget := max(1, cfg.TargetTokens-reserve)
	// The restored tail holds its start across appends like any other cutoff.
	alloc := workingSetAllocation{rows: len(tail) + 1, tokens: budget, bytes: math.MaxInt / 2}
	m.cutoffs.update(sess.ID, m.cutoffKey(ctx, sess, cfg, view), tail, func(st *cutoffState) {
		at, advanced := selectCutoff(tail, alloc, st.tail.ord)
		tail = tail[at:]
		if advanced {
			st.note(cutoffTailAdvanced)
		}
		st.tail = markRow(tail[0])
	})
	if len(tail) == 0 || tail[0].Ord <= 1 || tail[0].Ord > view.Continuation.ThroughOrd {
		return nil, false
	}
	record := restoredCoverage(view.Continuation, tail[0].Ord)
	record.Window = cfg.ModelContextWindow
	record.Receiver = sess.Model
	recall := m.recallAvailable != nil && m.recallAvailable(ctx, sess)
	continuationText, err := compaction.RenderContinuation(ctx, record, recall, 0)
	if err != nil {
		return nil, false
	}
	out := make([]api.Message, 0, len(tail)+2)
	for _, msg := range view.Messages {
		if len(msg.ContentParts) == 1 && msg.ContentParts[0].Source == "compaction_summary" {
			msg.Content = continuationText
			msg.ContentParts = append([]api.MessageContentPart(nil), msg.ContentParts...)
			msg.ContentParts[0].Content = continuationText
			out = append(out, msg)
			continue
		}
		if msg.Ord > 0 && msg.Ord < tail[0].Ord && msg.ContextPinned {
			if canonical, ok := byID[msg.ID]; ok {
				msg = api.RehydrateTranscriptProjectionFields(msg, canonical)
			}
			out = append(out, msg)
		}
	}
	out = append(out, tail...)
	return out, true
}

// restoredCoverage narrows a record to rows before the restored tail. Prose that
// reaches into the tail cannot be clipped, so only recovery references remain.
func restoredCoverage(prior *messageview.Continuation, tailStart int64) *messageview.Continuation {
	record := messageview.CloneContinuation(prior)
	record.ThroughOrd = tailStart - 1
	if record.SummaryThroughOrd > record.ThroughOrd {
		record.SummaryThroughOrd = 0
		record.Summary = messageview.ContinuationSummary{}
		record.Reason = "references_only"
	}
	filtered := make([]messageview.ContinuationSource, 0, len(record.Sources))
	for _, src := range record.Sources {
		if src.Ord <= 0 || src.Ord < tailStart {
			filtered = append(filtered, src)
		}
	}
	record.Sources = filtered
	return record
}

func estimateSingleMessageTokens(msg api.Message) int {
	return compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI([]api.Message{msg}))
}

// estimateSingleMessageBytes counts the text a request carries once: content,
// its tool-result copy, and its parts are one body. Call arguments add to it.
func estimateSingleMessageBytes(msg api.Message) int {
	size := len(msg.Content)
	if msg.ToolResult != nil {
		size = max(size, len(msg.ToolResult.Content))
	}
	parts := 0
	for _, part := range msg.ContentParts {
		parts += len(part.Content)
	}
	size = max(size, parts)
	if len(msg.ToolCalls) > 0 {
		if raw, err := json.Marshal(msg.ToolCalls); err == nil {
			size += len(raw)
		}
	}
	return max(1, size)
}
