package compaction

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTypedContinuationPreservedAcrossGenericChunkPasses(t *testing.T) {
	cfg := DefaultCompactionConfig()
	cfg.KeepRecentMessages = 1
	cfg.ChunkStrategyDefault = "truncate"

	continuationText := "## Continuation\n" + strings.Repeat("Recorded fact and recovery handle evidence#1. ", 220)
	if tokens := tokenest.EstimateDefault(continuationText); tokens < cfg.ChunkTokenThreshold {
		t.Fatalf("continuation fixture tokens = %d, want >= %d", tokens, cfg.ChunkTokenThreshold)
	}
	quotedHeadingText := "## Continuation\n" + strings.Repeat("Ordinary assistant prose quoting a heading. ", 220)
	if tokens := tokenest.EstimateDefault(quotedHeadingText); tokens < cfg.ChunkTokenThreshold {
		t.Fatalf("ordinary assistant fixture tokens = %d, want >= %d", tokens, cfg.ChunkTokenThreshold)
	}

	checkpoint := ContextMessage{
		ID:                       "cp-1",
		Role:                     string(api.MessageRoleUser),
		Content:                  compactionCheckpointContent(),
		ContentParts:             compactionCheckpointParts(),
		ContextMessageCompaction: ContextMessageCompaction{CompactionCheckpoint: true},
	}
	continuation := ContextMessage{
		ID:      "sum-1",
		Role:    string(api.MessageRoleAssistant),
		Content: continuationText,
		ContentParts: []api.MessageContentPart{{
			Content:   continuationText,
			Origin:    api.MessageOriginModel,
			Authority: api.ContentAuthorityNone,
			TrustTier: api.ContentTrustTierTrusted,
			Source:    "compaction_summary",
		}},
	}
	quotedAssistant := ContextMessage{
		ID:      "asst-quoted",
		Role:    string(api.MessageRoleAssistant),
		Content: quotedHeadingText,
	}
	tail := ContextMessage{
		ID:      "tail-1",
		Role:    string(api.MessageRoleUser),
		Content: "continue",
	}

	messages := []ContextMessage{checkpoint, continuation, quotedAssistant, tail}

	if class := ClassifyMessageDiet(ClassifyMessageDietInput{Index: 1, Messages: messages}); class.Eligibility != DietEligibilityPinned {
		t.Fatalf("continuation eligibility = %q, want %q", class.Eligibility, DietEligibilityPinned)
	}
	if class := ClassifyMessageDiet(ClassifyMessageDietInput{Index: 2, Messages: messages}); class.Eligibility != DietEligibilityEligible {
		t.Fatalf("ordinary assistant eligibility = %q, want %q", class.Eligibility, DietEligibilityEligible)
	}

	chunks := FindOversizedChunks(messages, cfg, nil)
	if len(chunks) != 1 || chunks[0].MessageID != "asst-quoted" {
		t.Fatalf("FindOversizedChunks = %+v, want only asst-quoted", chunks)
	}

	compactor := NewSimpleCompactor(cfg, UnavailableSummarizer{})
	out, compacted := compactor.CompactOversizedChunksOnly(t.Context(), SessionInfo{ID: "sess-1"}, messages)
	if compacted != 1 {
		t.Fatalf("compacted = %d, want 1", compacted)
	}
	if out[1].CompactedChunk != nil || !isContinuationSummary(out[1]) || out[1].Content != continuationText {
		t.Fatalf("typed continuation was modified: compactedChunk=%+v parts=%+v", out[1].CompactedChunk, out[1].ContentParts)
	}
	if out[2].CompactedChunk == nil || len(out[2].ContentParts) != 1 || out[2].ContentParts[0].Source != "compacted_chunk" {
		t.Fatalf("ordinary assistant row was not compacted: compactedChunk=%+v parts=%+v", out[2].CompactedChunk, out[2].ContentParts)
	}

	// A pass with only the oversized continuation as candidate must publish zero generic replacements.
	onlyContinuation := []ContextMessage{checkpoint, continuation, tail}
	if onlyChunks := FindOversizedChunks(onlyContinuation, cfg, nil); len(onlyChunks) != 0 {
		t.Fatalf("expected 0 candidates when only continuation is oversized, got %+v", onlyChunks)
	}
	outOnly, compactedOnly := compactor.CompactOversizedChunksOnly(t.Context(), SessionInfo{ID: "sess-1"}, onlyContinuation)
	if compactedOnly != 0 {
		t.Fatalf("compactedOnly = %d, want 0", compactedOnly)
	}
	if outOnly[1].CompactedChunk != nil || !isContinuationSummary(outOnly[1]) || outOnly[1].Content != continuationText {
		t.Fatalf("continuation modified on continuation-only pass: %+v", outOnly[1])
	}
}
