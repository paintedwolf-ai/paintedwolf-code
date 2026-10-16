package compaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

// SummaryBudget resolves the utility model independently of the receiver.
type SummaryBudget struct{ Input, Output int }

type summaryBudgetSource interface{ SummaryBudget() SummaryBudget }

func (c *SimpleCompactor) summaryBudget() SummaryBudget {
	budget := SummaryBudget{Input: c.cfg.SummaryInputTokens, Output: c.cfg.SummaryOutputTokens}
	if source, ok := c.summary.(summaryBudgetSource); ok {
		resolved := source.SummaryBudget()
		if resolved.Input > 0 {
			budget.Input = min(budget.Input, resolved.Input)
		}
		if resolved.Output > 0 {
			budget.Output = min(budget.Output, resolved.Output)
		}
	}
	return budget
}

// summarizePrefix accounts for every source even when synthesis is unavailable.
func (c *SimpleCompactor) summarizePrefix(ctx context.Context, info SessionInfo, prefix []ContextMessage) (compactionSummary, []messageview.ContinuationSource, error) {
	summary := priorSummary(info.PriorContinuation)
	sources, segments := continuationSegments(prefix, c.cfg.SummaryMessageTokens)
	if len(segments) == 0 {
		return summary, sources, nil
	}
	if c.summary == nil {
		return summary, sources, fmt.Errorf("summarizer unavailable")
	}
	system, err := guidance.RenderCompactionSystemPrompt(ctx, guidance.CompactionSessionSummarySystemRef)
	if err != nil {
		return summary, sources, err
	}
	budget := c.summaryBudget()
	calls, position := 0, 0
	remaining := map[string]int{}
	for _, segment := range segments {
		remaining[segment.ID]++
	}
	for position < len(segments) && calls < c.cfg.ChunkMaxPerPass {
		info.PriorContinuation = &messageview.Continuation{Summary: summaryRecord(summary)}
		inputBudget := budget.Input
		accepted := false
		for attempt := 0; attempt < 2 && calls < c.cfg.ChunkMaxPerPass; attempt++ {
			input, buildErr := BuildCompactionInput(ctx, info, segments[position:], inputBudget, c.cfg.SummaryMessageTokens)
			if buildErr != nil {
				err = buildErr
				break
			}
			calls++
			raw, callErr := c.summary.Summarize(WithSummaryFormat(ctx), system, input.Text, budget.Output)
			if ctx.Err() != nil {
				return summary, sources, ctx.Err()
			}
			if callErr != nil && !retryCompactionProviderError(callErr) {
				return summary, sources, callErr
			}
			var next compactionSummary
			if callErr == nil {
				next, callErr = parseCompactionSummary(raw)
			}
			if callErr != nil {
				err = callErr
				if errors.Is(callErr, context.Canceled) {
					break
				}
				inputBudget = min(inputBudget/2, c.cfg.SummaryRetryInputTokens)
				continue
			}
			next.SourceContext = sourceref.ForResponse(input.Messages, raw)
			summary = next
			for _, segment := range segments[position : position+input.Consumed] {
				remaining[segment.ID]--
				for i := range sources {
					if sources[i].MessageID == segment.ID {
						sources[i].Disposition = "partial_summary_and_reference"
						if remaining[segment.ID] == 0 {
							sources[i].Disposition = "summary_and_reference"
						}
					}
				}
			}
			position += input.Consumed
			accepted, err = true, nil
			break
		}
		if !accepted {
			break
		}
	}
	if position < len(segments) && err == nil {
		err = fmt.Errorf("summary work budget exhausted; remaining sources are referenced")
	}
	return summary, sources, err
}

func continuationSegments(rows []ContextMessage, tokens int) ([]messageview.ContinuationSource, []ContextMessage) {
	var sources []messageview.ContinuationSource
	var segments []ContextMessage
	width := max(1, tokens*tokenest.DefaultDivisor)
	for _, row := range rows {
		if row.CompactionCheckpoint || isContinuationSummary(row) {
			continue
		}
		content, _, _, _ := projectCompactionMessage(row)
		tool := row.ToolName
		if tool == "" && len(row.ToolCalls) == 1 {
			tool = row.ToolCalls[0].Name
		}
		sources = append(sources, messageview.ContinuationSource{MessageID: row.ID, Ord: row.Ord,
			Tool: tool, Handles: append([]string(nil), row.EvidenceHandles...),
			Disposition: "reference", Tokens: tokenest.EstimateDefault(content)})
		runes := []rune(content)
		for start := 0; start < len(runes); start += width {
			segment := row
			segment.Content = string(runes[start:min(start+width, len(runes))])
			segment.ContentParts = nil
			segment.Authority = api.ContentAuthorityNone
			segments = append(segments, segment)
		}
	}
	return sources, segments
}

func priorSummary(record *messageview.Continuation) compactionSummary {
	if record == nil {
		return compactionSummary{}
	}
	return compactionSummary{Facts: record.Summary.Facts, Completed: record.Summary.Completed,
		Pending: record.Summary.Pending, Reacquire: record.Summary.Reacquire, Constraints: record.Summary.Constraints}
}

func summaryRecord(summary compactionSummary) messageview.ContinuationSummary {
	return messageview.ContinuationSummary{Facts: summary.Facts, Completed: summary.Completed,
		Pending: summary.Pending, Reacquire: summary.Reacquire, Constraints: summary.Constraints}
}
