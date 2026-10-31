package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
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

func TestRegistrySummarizerSharedPrefixAndLocalDefault(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	coordProvider := &captureProvider{id: "anthropic"}
	localProvider := &captureProvider{id: IncludedProviderID}
	reg := newTestRegistry()
	testutil.FailErr(t, "Register anthropic", reg.Register(coordProvider))
	testutil.FailErr(t, "Register nativegen", reg.Register(localProvider))

	// Policy configures only the remote coordinator; Lite is unset and should default to nativegen.
	policy := NewInMemoryPolicyStore(ModelPolicy{
		Coordinator: ModelRef{ProviderID: "anthropic", Model: "claude-sonnet-4-6"},
	})
	summarizer := &RegistrySummarizer{
		Registry: reg,
		Policy:   policy,
		Purpose:  "compaction_turn",
		Scope:    SettingsScopeGlobal,
	}

	prefix := []api.Message{
		{Role: api.MessageRoleSystem, Content: "Coordinator system instructions.", Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted},
		{Role: api.MessageRoleUser, Content: "Inspect store.go"},
		{Role: api.MessageRoleAssistant, Content: "Checked store.go lease lifecycle."},
	}
	ctx := compaction.WithSummaryFormat(compaction.WithSharedPrefixMessages(context.Background(), prefix))
	_, err := summarizer.Summarize(ctx, "Synthesize continuation.", "Covered page.", 512)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if localProvider.last.Model != IncludedModelID {
		t.Fatalf("local model = %q, want %q", localProvider.last.Model, IncludedModelID)
	}
	if coordProvider.last.Model != "" {
		t.Fatalf("coordinator should not be called when local default is available, got model %q", coordProvider.last.Model)
	}
	if len(localProvider.last.Messages) != len(prefix)+1 {
		t.Fatalf("len(messages) = %d, want %d", len(localProvider.last.Messages), len(prefix)+1)
	}
	projectedPrefix := transcript.Project(prefix)
	for i := range projectedPrefix {
		if localProvider.last.Messages[i].Role != projectedPrefix[i].Role || localProvider.last.Messages[i].Content != projectedPrefix[i].Content {
			t.Fatalf("prefix message[%d] = %+v, want %+v", i, localProvider.last.Messages[i], projectedPrefix[i])
		}
	}
	tail := localProvider.last.Messages[len(localProvider.last.Messages)-1]
	if tail.Role != api.MessageRoleUser || !strings.Contains(tail.Content, "Synthesize continuation.") || !strings.Contains(tail.Content, "Covered page.") {
		t.Fatalf("trailing summary instruction message = %+v", tail)
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
