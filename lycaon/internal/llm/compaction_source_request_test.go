package llm

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompactionExternalProviderReceivesEvidenceWithoutLiveTools(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	external := &captureProvider{id: "external"}
	local := &captureProvider{id: IncludedProviderID}
	registry := newTestRegistry()
	testutil.FailErr(t, "register external", registry.Register(external))
	testutil.FailErr(t, "register included", registry.Register(local))
	publishIncludedModelFixture(t, registry, local)
	summarizer := &RegistrySummarizer{Registry: registry, Policy: NewInMemoryPolicyStore(ModelPolicy{
		Coordinator: ModelRef{ProviderID: IncludedProviderID, Model: "included"},
		Lite:        ModelRef{ProviderID: "external", Model: "external-model"},
	}), Purpose: "compaction_turn"}
	input, err := compaction.BuildCompactionInput(t.Context(), compaction.SessionInfo{}, []compaction.ContextMessage{
		{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "call-1", Name: "read", Args: map[string]any{"path": "unique-source.go"}}}},
		{Role: "system", Content: "progress event"},
		{Role: "tool", ToolCallID: "call-1", ToolName: "read", Content: "result body"},
	}, 4096, 256)
	testutil.FailErr(t, "build source page", err)
	_, err = summarizer.Summarize(compaction.WithSummaryFormat(t.Context()), "Summarize evidence.", input.Text, 512)
	testutil.FailErr(t, "summarize external", err)
	if external.last.Model != "external-model" || local.last.Model != "" {
		t.Fatal("explicit external selection changed")
	}
	if len(external.last.Messages) != 2 {
		t.Fatal("source page became live history")
	}
	for _, msg := range external.last.Messages {
		if msg.Role == api.MessageRoleTool || len(msg.ToolCalls) > 0 || msg.ToolResult != nil {
			t.Fatal("source data gained tool authority")
		}
	}
	body := external.last.Messages[1].Content
	if strings.Count(body, "unique-source.go") != 1 || !strings.Contains(body, "result_call_id") || !strings.Contains(body, "call-1") {
		t.Fatal("source evidence duplicated or lost")
	}
}

func TestCompactionFullRequestBudgetRefusesBeforeProvider(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	for _, large := range []string{"system", "source", "output"} {
		t.Run(large, func(t *testing.T) {
			provider := &captureProvider{id: "bounded"}
			registry := &Registry{}
			registry.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
				Providers: map[string]modelcall.Provider{"bounded": provider},
				Entries:   map[string]CatalogEntry{"bounded": {ID: "bounded", Models: []modelinfo.Entry{{ID: "model", ContextLength: 4096}}}},
			}))
			summarizer := &RegistrySummarizer{Registry: registry}
			system, source, output := "system", "source", 128
			switch large {
			case "system":
				system = strings.Repeat("x", 16384)
			case "source":
				source = strings.Repeat("x", 16384)
			case "output":
				output = 4096
			}
			_, err := summarizer.summarizeWith(compaction.WithSummaryFormat(t.Context()), provider, "model", system, source, output)
			if err == nil || provider.last.Model != "" {
				t.Fatal("over-budget request reached provider")
			}
		})
	}
}
