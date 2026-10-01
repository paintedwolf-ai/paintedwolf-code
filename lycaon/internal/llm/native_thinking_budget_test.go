package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	vertexexpressprovider "github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
)

func TestReasoningBudgetCannotExceedExplicitOutputLimit(t *testing.T) {
	for _, limit := range []int{512, 1024, 2048, 8192} {
		req := modelcall.CompletionRequest{Model: "fixture", MaxTokens: limit, Think: modelcall.ThinkHigh}
		anthropic := anthropicprovider.New("fixture", "https://example.invalid", "", []modelinfo.Entry{{ID: "fixture", ThinkStyle: "budget_tokens"}}).Prepare(req, false)
		if anthropic.MaxTokens != limit || (anthropic.Thinking != nil && anthropic.Thinking.BudgetTokens >= limit) {
			t.Fatalf("Anthropic exceeded %d: %+v", limit, anthropic)
		}
		vertex := vertexexpressprovider.New("vertex-express", "https://aiplatform.googleapis.com/v1", "k", []modelinfo.Entry{{ID: "fixture"}}).Prepare(req).GenerationConfig
		if vertex.MaxOutputTokens != limit || (vertex.ThinkingConfig != nil && vertex.ThinkingConfig.ThinkingBudget != nil && *vertex.ThinkingConfig.ThinkingBudget >= limit) {
			t.Fatalf("Vertex exceeded %d: %+v", limit, vertex)
		}
	}
}
