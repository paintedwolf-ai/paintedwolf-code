package compaction

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/lycaon/lycaon/internal/messageview"
)

type reductionPage struct {
	source      []ContextMessage
	retained    []ContextMessage
	replacement []ContextMessage
	record      *messageview.Continuation
}

// reductionPage bounds source work while keeping each producing call with its results.
func (c *SimpleCompactor) reductionPage(ctx context.Context, info SessionInfo, all, eligible []ContextMessage) (reductionPage, error) {
	limit := max(1, c.cfg.MessageSlice)
	maxRows := min(messageview.MaxCompactionRows, limit*4)
	maxTokens := max(c.cfg.SummaryInputTokens*max(1, c.cfg.ChunkMaxPerPass), c.cfg.SessionMinSavingsTokens*4)
	maxBytes := maxTokens * 4
	bounded := boundedReductionPrefix(eligible, maxRows, maxTokens, maxBytes)
	var page reductionPage
	for len(bounded) > 0 {
		prefix := oldestCompactionPrefix(bounded, min(limit, len(bounded)))
		sources, _ := continuationSegments(prefix, c.cfg.SummaryMessageTokens)
		record := continuationFor(info, prefix, priorSummary(info.PriorContinuation), sources, ErrNoLiteProvider)
		record.Window = c.cfg.ModelContextWindow
		c.rankContinuation(ctx, info, record)
		text, err := RenderContinuation(ctx, record, info.RecallAvailable, 0)
		if err != nil {
			return reductionPage{}, err
		}
		page = reductionPage{source: prefix, retained: remainingCompactionRows(all, retainedMessageIDs(prefix)), record: record}
		page.replacement = compactionReplacement(info, prefix, nil, priorSummary(info.PriorContinuation), text)
		useful, err := c.usefulCompaction(info, prefix, page.replacement, c.cfg.SessionMinSavingsTokens)
		if err != nil || useful || len(prefix) == len(bounded) {
			return page, err
		}
		limit = min(len(bounded), max(limit+1, limit*2))
	}
	return page, nil
}

func boundedReductionPrefix(rows []ContextMessage, maxRows, maxTokens, maxBytes int) []ContextMessage {
	end, tokens, bytes := 0, 0, 0
	for end < len(rows) {
		next := end + 1
		for next < len(rows) && rows[next].Role == "tool" {
			next++
		}
		batch := rows[end:next]
		batchTokens := EstimateMessagesTokens(batch)
		encoded, err := json.Marshal(batch)
		if err != nil || next > maxRows || tokens+batchTokens > maxTokens || bytes+len(encoded) > maxBytes {
			break
		}
		end, tokens, bytes = next, tokens+batchTokens, bytes+len(encoded)
	}
	return rows[:end]
}

func (c *SimpleCompactor) usefulReduction(info SessionInfo, page reductionPage, all, candidate []ContextMessage) (bool, error) {
	useful, err := c.usefulCompaction(info, page.source, page.replacement, c.cfg.SessionMinSavingsTokens)
	if err != nil || !useful {
		return false, err
	}
	measured, err := measureCompactionPair(info, all, candidate)
	return err == nil && EstimateMessagesTokens(candidate) < EstimateMessagesTokens(all) && measured.After < measured.Before, err
}

func reportReductionPage(ctx context.Context, info SessionInfo, page reductionPage, accepted bool) {
	source, replacement := EstimateMessagesTokens(page.source), EstimateMessagesTokens(page.replacement)
	slog.DebugContext(ctx, "session reduction page evaluated", "session_id", info.ID,
		"source_rows", len(page.source), "source_tokens", source,
		"replacement_tokens", replacement, "net_savings_tokens", source-replacement,
		"accepted", accepted)
}
