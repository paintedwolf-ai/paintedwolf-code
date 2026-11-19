package compaction

import (
	"context"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/pkg/api"
)

// sessionReduction is one evaluated page. summaryFailure names a provider failure
// behind a rejected page; it stays retryable and leaves the memo untouched.
type sessionReduction struct {
	messages       []ContextMessage
	continuation   *messageview.Continuation
	reason         string
	cacheHit       bool
	summaryFailure error
}

// compactSession replaces a bounded oldest prefix and retains its unprocessed suffix.
func (c *SimpleCompactor) compactSession(ctx context.Context, info SessionInfo, messages []ContextMessage, target int) (sessionReduction, error) {
	retained := retainedCompactionMessages(messages, recentCompactionCount(c.cfg, messages, target))
	removed := beforeWorkingSet(removedCompactionMessages(messages, retained), info.WorkingSetStartOrd)
	if len(removed) == 0 {
		return sessionReduction{reason: "protected_history"}, nil
	}
	page, err := c.reductionPage(ctx, info, messages, removed)
	if err != nil {
		return sessionReduction{reason: "failed"}, err
	}
	if len(page.source) == 0 {
		return sessionReduction{reason: "page_budget"}, nil
	}
	possible, err := c.usefulCompaction(info, page.source, compactionReplacement(info, page.source, nil, compactionSummary{}, ""), c.cfg.SessionMinSavingsTokens)
	if err != nil || !possible {
		return sessionReduction{reason: "insufficient_reclaimable_space"}, err
	}
	revision, err := c.sessionRevision(ctx, info, page.source)
	if err != nil {
		return sessionReduction{reason: "failed"}, err
	}
	if info.Attempts != nil {
		prior, found, err := info.Attempts.GetCompactionAttempt(ctx, info.ID)
		if err != nil {
			return sessionReduction{reason: "failed"}, err
		}
		if found && prior.Revision == revision {
			return sessionReduction{reason: prior.Reason, cacheHit: true}, nil
		}
	}
	summary, sources, summaryErr := c.summarizePrefix(ctx, info, page.source, nil)
	if ctx.Err() != nil {
		return sessionReduction{reason: "failed"}, ctx.Err()
	}
	record := continuationFor(info, page.source, summary, sources, summaryErr)
	record.Window = c.cfg.ModelContextWindow
	c.rankContinuation(ctx, info, record)
	text, err := RenderContinuation(ctx, record, info.RecallAvailable, 0)
	if err != nil {
		return sessionReduction{reason: "failed"}, err
	}
	page.replacement = compactionReplacement(info, page.source, nil, summary, text)
	candidate := append(append([]ContextMessage(nil), page.replacement...), page.retained...)
	useful, err := c.usefulReduction(info, page, messages, candidate)
	reportReductionPage(ctx, info, page, useful && err == nil)
	if err != nil {
		return sessionReduction{reason: "failed"}, err
	}
	if !useful {
		if info.Attempts != nil && (summaryErr == nil || c.summary == nil) {
			if err := info.Attempts.PutCompactionAttempt(ctx, info.ID, messageview.CompactionAttempt{Revision: revision, Reason: "insufficient_savings"}); err != nil {
				return sessionReduction{reason: "failed"}, err
			}
		}
		rejected := sessionReduction{reason: "insufficient_savings"}
		if c.summary != nil {
			rejected.summaryFailure = summaryErr
		}
		return rejected, nil
	}
	reason := "partial_reduction"
	if EstimateMessagesTokens(candidate) <= target {
		reason = "target_reached"
	}
	return sessionReduction{messages: candidate, continuation: record, reason: reason}, nil
}

// beforeWorkingSet limits summary eligibility; published coverage never reaches the recent suffix.
func beforeWorkingSet(rows []ContextMessage, startOrd int64) []ContextMessage {
	if startOrd <= 0 {
		return rows
	}
	draftOrds := make(map[string]int64)
	for _, row := range rows {
		if row.Role == string(api.MessageRoleAssistant) && row.ID != "" && row.Ord > 0 {
			draftOrds[row.ID] = row.Ord
		}
	}
	out := make([]ContextMessage, 0, len(rows))
	for _, row := range rows {
		ord := row.Ord
		if row.DraftRecoveryTarget != "" {
			if draftOrd, ok := draftOrds[row.DraftRecoveryTarget]; ok && draftOrd < ord {
				ord = draftOrd
			}
		}
		if ord < startOrd {
			out = append(out, row)
		}
	}
	return out
}

func oldestCompactionPrefix(rows []ContextMessage, limit int) []ContextMessage {
	if limit <= 0 || len(rows) <= limit {
		return rows
	}
	end := limit
	if target := rows[end-1].DraftRecoveryTarget; target != "" { //nolint:gosec // G602 — the guard above returns when len(rows) <= limit, so end-1 stays within rows.
		for end < len(rows) && rows[end].DraftRecoveryTarget == target {
			end++
		}
		if end < len(rows) && rows[end].ID == target {
			end++
		}
	}
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
	record := &messageview.Continuation{Format: messageview.ContinuationFormat, Sources: mergeContinuationSources(info.PriorContinuation, sources),
		Receiver: info.Model, Reason: "summarized"}
	if info.PriorContinuation != nil {
		record.ThroughOrd = info.PriorContinuation.ThroughOrd
		record.RemovedRows = info.PriorContinuation.RemovedRows
		record.RecordedRows = info.PriorContinuation.RecordedRows
		record.SummaryThroughOrd = info.PriorContinuation.SummaryThroughOrd
	}
	for _, row := range removed {
		if row.Ord > record.ThroughOrd {
			record.ThroughOrd = row.Ord
		}
		if !row.CompactionCheckpoint && !isContinuationSummary(row) {
			record.RemovedRows++
		}
	}
	for _, source := range sources {
		if source.HasRecordedDetail() && (info.PriorContinuation == nil || source.Ord > info.PriorContinuation.ThroughOrd) {
			record.RecordedRows++
		}
		if source.Disposition != "reference" && source.Ord > record.SummaryThroughOrd {
			record.SummaryThroughOrd = source.Ord
		}
	}
	if summaryErr != nil || record.SummaryThroughOrd < record.ThroughOrd {
		record.Reason = "references_only"
	}
	record.Summary = messageview.ContinuationSummary{Facts: summary.Facts, Completed: summary.Completed,
		Pending: summary.Pending, Reacquire: summary.Reacquire, Constraints: summary.Constraints}
	return record
}

func isContinuationSummary(row ContextMessage) bool {
	return len(row.ContentParts) == 1 && row.ContentParts[0].Source == "compaction_summary"
}
