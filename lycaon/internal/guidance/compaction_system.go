package guidance

import (
	"context"

	"github.com/lycaon/lycaon/internal/prompts"
)

const (
	CompactionSessionSummarySystemRef = "compaction/session-summary-system"
	CompactionChunkSummarySystemRef   = "compaction/chunk-summary-system"
	// CompactionContinuationRecordRef renders the record the model reads after
	// compaction: the summarizer's lines plus the host's way back to the rows
	// the summary replaced.
	CompactionContinuationRecordRef = "compaction/continuation-record"
)

// RenderCompactionSystemPrompt renders a lite-model system prompt under guidance/compaction/.
func RenderCompactionSystemPrompt(ctx context.Context, ref string) (string, error) {
	return prompts.RenderUtilityPrompt(ctx, ref, nil)
}
