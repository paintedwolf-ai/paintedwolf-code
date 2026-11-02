package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
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
			if !errors.Is(err, failure.ErrProviderContextTooSmall) || provider.last.Model != "" {
				t.Fatal("over-budget request reached provider")
			}
		})
	}
}

type compactionBudgetFixture struct {
	summarizer         *RegistrySummarizer
	provider           *captureProvider
	attempts, refusals int
	oversizedSystem    bool
}

func (f *compactionBudgetFixture) Summarize(ctx context.Context, system, source string, output int) (string, error) {
	f.attempts++
	if f.oversizedSystem {
		system = strings.Repeat("x", 16384)
	}
	result, err := f.summarizer.summarizeWith(ctx, f.provider, "model", system, source, output)
	if errors.Is(err, failure.ErrProviderContextTooSmall) {
		f.refusals++
	}
	return result, err
}
func TestCompactionBudgetGuardReducesPagesOrRetainsReferences(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	for _, impossible := range []bool{false, true} {
		t.Run(fmt.Sprint(impossible), func(t *testing.T) {
			provider := &captureProvider{id: "bounded", response: `{"facts":["observed"],"completed":[],"pending":[],"reacquire":[],"constraints":[]}`}
			registry := &Registry{}
			registry.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
				Providers: map[string]modelcall.Provider{"bounded": provider},
				Entries:   map[string]CatalogEntry{"bounded": {ID: "bounded", Models: []modelinfo.Entry{{ID: "model", ContextLength: 4096}}}},
			}))
			fixture := &compactionBudgetFixture{summarizer: &RegistrySummarizer{Registry: registry, Purpose: "compaction_turn"}, provider: provider, oversizedSystem: impossible}
			cfg := testCompactionConfig()
			cfg.ChunkTokenThreshold = 1000000
			cfg.KeepRecentMessages = 1
			cfg.ChunkMaxPerPass = 2
			cfg.SummaryInputTokens = 4096
			cfg.SummaryRetryInputTokens = 2048
			cfg.SummaryMessageTokens = 64
			cfg.SummaryOutputTokens = 128
			var rows []compaction.ContextMessage
			for i := range 60 {
				rows = append(rows, compaction.ContextMessage{ID: fmt.Sprint(i), Ord: int64(i + 1), Role: "user", Content: strings.Repeat("evidence ", 100)})
			}
			rows = append(rows, compaction.ContextMessage{ID: "current", Ord: 61, Role: "user", Content: "continue"})
			_, report, err := compaction.NewSimpleCompactor(cfg, fixture).Compact(t.Context(), compaction.SessionInfo{ID: "budget"}, rows, 120)
			testutil.FailErr(t, "compact bounded source", err)
			if !report.SessionCompacted || fixture.attempts != 2 || fixture.refusals < 1 {
				t.Fatalf("missing bounded retry: attempts=%d refusals=%d report=%+v", fixture.attempts, fixture.refusals, report)
			}
			if impossible && provider.last.Model != "" {
				t.Fatal("impossible envelope reached provider")
			}
			if !impossible && (fixture.refusals != 1 || provider.last.Model != "model") {
				t.Fatal("smaller page did not reach provider")
			}
		})
	}
}
