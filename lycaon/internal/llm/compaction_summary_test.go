package llm

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/transcript"
)

func TestRegistrySummarizerRequestsCompactionSchema(t *testing.T) {
	provider := &captureProvider{id: "lite"}
	r := newTestRegistrySummarizer(t, provider)
	_, err := r.Summarize(compaction.WithSummaryFormat(context.Background()), "system", "transcript", 321)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if provider.last.ResponseFormat == nil || provider.last.ResponseFormat.Name != "compaction_summary" {
		t.Fatalf("response format = %+v", provider.last.ResponseFormat)
	}
	if provider.last.MaxTokens != 321 {
		t.Fatalf("MaxTokens = %d", provider.last.MaxTokens)
	}
	if len(provider.last.Messages) != 2 || !transcript.HasAuthorityNotice(provider.last.Messages[0]) {
		t.Fatalf("messages = %+v", provider.last.Messages)
	}
}
