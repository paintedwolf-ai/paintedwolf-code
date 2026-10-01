package guidance

import (
	"context"
	"fmt"
	"strings"
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
	block, err := RenderGuidance(ctx, ref, nil)
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" {
		return "", fmt.Errorf("compaction system prompt %q rendered empty", ref)
	}
	return block, nil
}
