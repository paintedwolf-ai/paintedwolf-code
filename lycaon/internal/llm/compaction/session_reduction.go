package compaction

import (
	"context"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

// compactSession replaces a bounded oldest prefix and retains its unprocessed suffix.
func (c *SimpleCompactor) compactSession(ctx context.Context, info SessionInfo, messages []ContextMessage, target int) ([]ContextMessage, *messageview.Continuation, string, bool, error) {
	retained := retainedCompactionMessages(messages, c.cfg.KeepRecentMessages)
	removed := removedCompactionMessages(messages, retained)
	if len(removed) == 0 {
		return nil, nil, "protected_history", false, nil
	}
	possible, err := c.usefulCompaction(info, messages, compactionReplacement(info, messages, retained, compactionSummary{}, ""), c.cfg.SessionMinSavingsTokens)
	if err != nil || !possible {
		return nil, nil, "insufficient_reclaimable_space", false, err
	}
	revision, err := c.sessionRevision(ctx, info, messages, target)
	if err != nil {
		return nil, nil, "failed", false, err
	}
	if info.Attempts != nil {
		prior, found, err := info.Attempts.GetCompactionAttempt(ctx, info.ID)
		if err != nil {
			return nil, nil, "failed", false, err
		}
		if found && prior.Revision == revision {
			return nil, nil, prior.Reason, true, nil
		}
	}
	prefix := oldestCompactionPrefix(removed, c.cfg.MessageSlice)
	keptIDs := retainedMessageIDs(prefix)
	// Restore canonical ordering when pins precede the unprocessed suffix.
	retained = remainingCompactionRows(messages, keptIDs)
	summary, sources, summaryErr := c.summarizePrefix(ctx, info, prefix)
	if ctx.Err() != nil {
		return nil, nil, "failed", false, ctx.Err()
	}
	record := continuationFor(info, prefix, summary, sources, summaryErr)
	record.Window = c.cfg.ModelContextWindow
	text, err := renderCoveredContinuation(ctx, record, info.RecallAvailable)
	if err != nil {
		return nil, nil, "failed", false, err
	}
	candidate := compactionReplacement(info, messages, retained, summary, text)
	useful, err := c.usefulCompaction(info, messages, candidate, c.cfg.SessionMinSavingsTokens)
	if err != nil {
		return nil, nil, "failed", false, err
	}
	if !useful {
		if info.Attempts != nil {
			if err := info.Attempts.PutCompactionAttempt(ctx, info.ID, messageview.CompactionAttempt{Revision: revision, Reason: "insufficient_savings"}); err != nil {
				return nil, nil, "failed", false, err
			}
		}
		return nil, nil, "insufficient_savings", false, nil
	}
	reason := "partial_reduction"
	if EstimateMessagesTokens(candidate) <= target {
		reason = "target_reached"
	}
	return candidate, record, reason, false, nil
}

func oldestCompactionPrefix(rows []ContextMessage, limit int) []ContextMessage {
	if limit <= 0 || len(rows) <= limit {
		return rows
	}
	end := limit
	for end < len(rows) && rows[end].Role == string(api.MessageRoleTool) {
		end++
	}
	return rows[:end]
}

func remainingCompactionRows(rows []ContextMessage, removed map[string]struct{}) []ContextMessage {
	out := make([]ContextMessage, 0, len(rows))
	for _, row := range rows {
		if _, found := removed[row.ID]; !found {
			out = append(out, row)
		}
	}
	return out
}

func continuationFor(info SessionInfo, removed []ContextMessage, summary compactionSummary, sources []messageview.ContinuationSource, summaryErr error) *messageview.Continuation {
	record := &messageview.Continuation{Format: messageview.ContinuationFormat, Sources: sources,
		Receiver: info.Model, Reason: "summarized"}
	if info.PriorContinuation != nil {
		record.ThroughOrd = info.PriorContinuation.ThroughOrd
		record.RemovedRows = info.PriorContinuation.RemovedRows
	}
	for _, row := range removed {
		if row.Ord > record.ThroughOrd {
			record.ThroughOrd = row.Ord
		}
		if !row.CompactionCheckpoint && !isContinuationSummary(row) {
			record.RemovedRows++
		}
	}
	if summaryErr != nil {
		record.Reason = "references_only"
	}
	record.Summary = messageview.ContinuationSummary{Facts: summary.Facts, Completed: summary.Completed,
		Pending: summary.Pending, Reacquire: summary.Reacquire, Constraints: summary.Constraints}
	return record
}

func isContinuationSummary(row ContextMessage) bool {
	return len(row.ContentParts) == 1 && row.ContentParts[0].Source == "compaction_summary"
}

func renderCoveredContinuation(ctx context.Context, record *messageview.Continuation, recall bool) (string, error) {
	budget := min(4096, max(192, record.Window/8))
	for _, level := range []string{"detailed", "compact", "references"} {
		if level == "detailed" && record.Window > 0 && record.Window < 16384 {
			continue
		}
		summary := priorSummary(record)
		summary.ThroughOrd = record.ThroughOrd
		summary.ReferenceOnly = record.Reason == "references_only"
		summary.Level = level
		limit := 16
		if level == "compact" {
			summary.Facts = summary.Facts[:min(2, len(summary.Facts))]
			summary.Completed = summary.Completed[:min(1, len(summary.Completed))]
			summary.Pending = summary.Pending[:min(3, len(summary.Pending))]
			summary.Constraints = summary.Constraints[:min(3, len(summary.Constraints))]
			summary.Reacquire = nil
			limit = 4
		}
		if level == "references" {
			summary = compactionSummary{Level: level, ThroughOrd: record.ThroughOrd, ReferenceOnly: true}
			limit = 0
		}
		var sources []CompactedSource
		for _, source := range record.Sources[max(0, len(record.Sources)-limit):] {
			sources = append(sources, CompactedSource{Handles: source.Handles[:min(4, len(source.Handles))], Tool: source.Tool, MessageID: source.MessageID})
		}
		text, err := renderContinuationRecord(ctx, summary, sources, recall)
		if err != nil {
			return "", err
		}
		if tokenest.EstimateDefault(text) <= budget || level == "references" {
			record.Level = level
			return text, nil
		}
	}
	panic("unreachable continuation level")
}
