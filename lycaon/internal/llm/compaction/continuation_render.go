package compaction

import (
	"context"
	"sort"
	"strconv"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/tokenest"
)

const maxRecordedDetails = 16

// RenderContinuation selects a representation within the receiver's budget.
func RenderContinuation(ctx context.Context, record *messageview.Continuation, recall bool, tokens int) (string, error) {
	budget := min(4096, max(192, record.Window/8))
	if record.RenderTokens > 0 {
		budget = min(budget, record.RenderTokens)
	}
	if tokens > 0 {
		budget = min(budget, tokens)
	}
	details := recordedDetails(record.Sources)
	for _, level := range []string{"detailed", "compact", "references"} {
		if level == "detailed" && record.Window > 0 && record.Window < 16384 {
			continue
		}
		display := continuationDisplayFor(record, level, details)
		text, err := display.fit(ctx, recall, budget)
		if err != nil {
			return "", err
		}
		if text != "" {
			record.Level = level
			return text, nil
		}
	}
	panic("unreachable continuation level")
}

type continuationDisplay struct {
	summary compactionSummary
	sources []CompactedSource
	details []map[string]any
	total   int
	clipped bool
}

func continuationDisplayFor(record *messageview.Continuation, level string, details []map[string]any) continuationDisplay {
	summary := priorSummary(record)
	summary.ThroughOrd = record.ThroughOrd
	summary.SummaryThroughOrd = record.SummaryThroughOrd
	summary.ReferenceOnly = record.Reason == "references_only"
	summary.Level = level
	limit := 16
	switch level {
	case "compact":
		summary.Facts = summary.Facts[:min(2, len(summary.Facts))]
		summary.Completed = summary.Completed[:min(1, len(summary.Completed))]
		summary.Pending = summary.Pending[:min(3, len(summary.Pending))]
		summary.Constraints = summary.Constraints[:min(3, len(summary.Constraints))]
		summary.Reacquire = nil
		limit = 4
	case "references":
		summary = compactionSummary{Level: level, ThroughOrd: record.ThroughOrd, ReferenceOnly: summary.ReferenceOnly}
		limit = 2
	}
	return continuationDisplay{
		summary: summary, sources: recoverySources(record.Sources, limit),
		details: details, total: max(record.RecordedRows, len(details)), clipped: record.RecordedRowsClipped,
	}
}

func (d continuationDisplay) fit(ctx context.Context, recall bool, budget int) (string, error) {
	fits := func(text string) bool {
		return tokenest.FromUnitCount(len(text), tokenest.DefaultDivisor) <= budget
	}
	base, err := d.render(ctx, recall, 0)
	if err != nil {
		return "", err
	}
	if !fits(base) {
		if d.summary.Level != "references" {
			return "", nil
		}
		d.sources = nil
		base, err = d.render(ctx, recall, 0)
		if err != nil || !fits(base) {
			return base, err
		}
	}
	for low, high := 1, len(d.details); low <= high; {
		count := low + (high-low)/2
		text, err := d.render(ctx, recall, count)
		if err != nil {
			return "", err
		}
		if fits(text) {
			base, low = text, count+1
		} else {
			high = count - 1
		}
	}
	return base, nil
}

func (d continuationDisplay) render(ctx context.Context, recall bool, count int) (string, error) {
	return renderContinuation(ctx, d.summary, d.sources, recall, d.details[:count], d.total-count, d.clipped)
}

func recordedDetails(sources []messageview.ContinuationSource) []map[string]any {
	var recorded []messageview.ContinuationSource
	for _, source := range sources {
		if source.HasRecordedDetail() {
			recorded = append(recorded, source)
		}
	}
	sort.SliceStable(recorded, func(i, j int) bool {
		return recorded[i].Ord < recorded[j].Ord
	})
	details := make([]map[string]any, 0, min(len(recorded), maxRecordedDetails))
	for _, source := range recorded[:cap(details)] {
		details = append(details, recordedDetailVars(source))
	}
	return details
}

func recordedDetailVars(source messageview.ContinuationSource) map[string]any {
	detail := map[string]any{"message_id": source.MessageID}
	if len(source.Handles) > 0 {
		detail["handle"] = source.Handles[0]
	}
	if source.Conclusion != "" {
		detail["conclusion"] = strconv.Quote(source.Conclusion)
	}
	if source.Command != "" {
		detail["command"] = strconv.Quote(source.Command)
	}
	if execution := source.Execution; execution != nil {
		detail["verdict"] = execution.SourceVerdict
		detail["program_state"] = execution.ProgramState
		detail["stop_code"] = execution.StopCode
		if execution.ExitCode != nil {
			detail["exit_code"] = strconv.Itoa(*execution.ExitCode)
		}
	}
	return detail
}

// recoverySources keeps a bounded set of evidence and original-call addresses.
func recoverySources(rows []messageview.ContinuationSource, limit int) []CompactedSource {
	limit = max(2, limit)
	var out []CompactedSource
	seen := make(map[string]bool)
	for _, singleEach := range []bool{true, false} {
		for _, allowRecall := range []bool{false, true} {
			for _, wantHandles := range []bool{true, false} {
				for step := 0; step < len(rows); step++ {
					i := step
					if !singleEach {
						i = len(rows) - 1 - step
					}
					row := rows[i]
					hasHandles := len(row.Handles) > 0
					isRecall := row.Tool == "recall"
					if seen[row.MessageID] || row.MessageID == "" || isRecall != allowRecall || hasHandles != wantHandles || !hasHandles && row.Tool == "" {
						continue
					}
					seen[row.MessageID] = true
					out = append(out, CompactedSource{Handles: row.Handles[:min(2, len(row.Handles))], Tool: row.Tool, MessageID: row.MessageID})
					if len(out) >= limit {
						return out
					}
					if singleEach {
						break
					}
				}
			}
		}
	}
	for i := len(rows) - 1; i >= 0 && len(out) < limit; i-- {
		row := rows[i]
		if !seen[row.MessageID] && row.MessageID != "" {
			out = append(out, CompactedSource{Handles: row.Handles[:min(2, len(row.Handles))], Tool: row.Tool, MessageID: row.MessageID})
			seen[row.MessageID] = true
		}
	}
	return out
}
