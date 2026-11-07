package llm

import (
	"context"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRegistrySummarizerSourceDataAndLocalDefault(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	coordProvider := &captureProvider{id: "anthropic"}
	localProvider := &captureProvider{id: IncludedProviderID}
	reg := newTestRegistry()
	testutil.FailErr(t, "Register anthropic", reg.Register(coordProvider))
	testutil.FailErr(t, "Register nativegen", reg.Register(localProvider))
	publishIncludedModelFixture(t, reg, localProvider)

	// An unset Lite role uses the included model.
	policy := NewInMemoryPolicyStore(ModelPolicy{
		Coordinator: ModelRef{ProviderID: "anthropic", Model: "claude-sonnet-4-6"},
	})
	summarizer := &RegistrySummarizer{
		Registry: reg,
		Policy:   policy,
		Purpose:  "compaction_turn",
		Scope:    SettingsScopeGlobal,
	}

	ctx := compaction.WithSummaryFormat(context.Background())
	_, err := summarizer.Summarize(ctx, "Synthesize continuation.", "Covered page.", 512)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if localProvider.last.Model != nativegen.DefaultModelID {
		t.Fatalf("local model = %q, want %q", localProvider.last.Model, nativegen.DefaultModelID)
	}
	if coordProvider.last.Model != "" {
		t.Fatalf("coordinator should not be called when local default is available, got model %q", coordProvider.last.Model)
	}
	if len(localProvider.last.Messages) != 2 {
		t.Fatal("summary must have only system and source-data messages")
	}
	if !strings.Contains(localProvider.last.Messages[0].Content, "Synthesize continuation.") ||
		!strings.Contains(localProvider.last.Messages[1].Content, "Covered page.") {
		t.Fatal("summary input lost instructions or source")
	}

}

func TestCompactionPromptPlacesCoveredSourceBeforeInstructions(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	input, err := compaction.BuildCompactionInput(
		context.Background(),
		compaction.SessionInfo{},
		[]compaction.ContextMessage{
			{Role: "user", Content: "first turn"},
			{Role: "assistant", Content: "second turn"},
		},
		4096,
		256,
	)
	if err != nil {
		t.Fatalf("BuildCompactionInput: %v", err)
	}
	coveredIdx := strings.Index(input.Text, "## Covered source page")
	instrIdx := strings.Index(input.Text, "## Summary instructions")
	schemaIdx := strings.Index(input.Text, "## Output schema")
	if coveredIdx < 0 || instrIdx < 0 || schemaIdx < 0 {
		t.Fatalf("missing sections in prompt:\n%s", input.Text)
	}
	if !(coveredIdx < instrIdx && instrIdx < schemaIdx) {
		t.Fatalf("expected Covered source page (%d) < Summary instructions (%d) < Output schema (%d)", coveredIdx, instrIdx, schemaIdx)
	}
}

func TestSimpleCompactorKeepsSourceOutOfLiveHistory(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	summaryJSON, err := (compaction.MockSummarizer{Text: "Compacted session state."}).Summarize(
		compaction.WithSummaryFormat(t.Context()), "", "", 0,
	)
	testutil.FailErr(t, "encode mock summary", err)

	coordProvider := &captureProvider{id: "anthropic", response: summaryJSON}
	localProvider := &captureProvider{id: IncludedProviderID, response: summaryJSON}
	reg := newTestRegistry()
	testutil.FailErr(t, "Register anthropic", reg.Register(coordProvider))
	testutil.FailErr(t, "Register nativegen", reg.Register(localProvider))
	publishIncludedModelFixture(t, reg, localProvider)

	policy := NewInMemoryPolicyStore(ModelPolicy{
		Coordinator: ModelRef{ProviderID: "anthropic", Model: "claude-sonnet-4-6"},
	})
	summarizer := &RegistrySummarizer{
		Registry: reg,
		Policy:   policy,
		Purpose:  "compaction_turn",
		Scope:    SettingsScopeGlobal,
	}

	cfg := testCompactionConfig()
	cfg.ChunkTokenThreshold = 1000000
	cfg.KeepRecentMessages = 1
	compactor := compaction.NewSimpleCompactor(cfg, summarizer)

	messages := []compaction.ContextMessage{
		{ID: "m1", Ord: 1, Role: "user", Content: strings.Repeat("inspect modelstore lease pins ", 120)},
		{ID: "m2", Ord: 2, Role: "assistant", Content: strings.Repeat("verified supplementary artifacts ", 120)},
		{ID: "m3", Ord: 3, Role: "user", Content: "continue with router wiring"},
	}
	_, report, err := compactor.Compact(t.Context(), compaction.SessionInfo{ID: "sess-1"}, messages, 120)
	testutil.FailErr(t, "compactor.Compact", err)
	if !report.SessionCompacted {
		t.Fatalf("expected session compaction, got report=%+v", report)
	}
	if localProvider.last.Model != nativegen.DefaultModelID {
		t.Fatalf("local model = %q, want %q", localProvider.last.Model, nativegen.DefaultModelID)
	}
	// Summary pages are data even when their source is conversation history.
	if len(localProvider.last.Messages) != 2 {
		t.Fatalf("expected system and source-data messages, got %d messages: %+v",
			len(localProvider.last.Messages), localProvider.last.Messages)
	}
	tail := localProvider.last.Messages[len(localProvider.last.Messages)-1]
	if tail.Role != api.MessageRoleUser || !strings.Contains(tail.Content, "## Covered source page") {
		t.Fatalf("trailing summary instruction message = %+v", tail)
	}
}
