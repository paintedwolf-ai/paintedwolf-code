package vertexexpress

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFixedNativeBudgetAndDisableWire(t *testing.T) {
	budget, off := 4096, false
	o := modelcall.ThinkingOverride{Mode: "fixed", BudgetTokens: &budget}
	vertex := vertexExpressGenerationConfig{MaxOutputTokens: 8192}
	applyFixedVertexThinking(&vertex, o)
	if *vertex.ThinkingConfig.ThinkingBudget != budget {
		t.Fatal("native Gemini budget changed")
	}
	applyFixedVertexThinking(&vertex, modelcall.ThinkingOverride{Mode: "fixed", Effort: "low"})
	data, err := json.Marshal(vertex.ThinkingConfig)
	testutil.FailErr(t, "marshal thinking level", err)
	if string(data) != `{"thinkingLevel":"low","includeThoughts":true}` {
		t.Fatalf("Gemini sent incompatible controls together: %s", data)
	}
	applyFixedVertexThinking(&vertex, modelcall.ThinkingOverride{Mode: "fixed", Enabled: &off})
	data, err = json.Marshal(vertex.ThinkingConfig)
	testutil.FailErr(t, "marshal thinking off", err)
	if string(data) != `{"thinkingBudget":0}` {
		t.Fatalf("Gemini disable omitted: %s", data)
	}
}
