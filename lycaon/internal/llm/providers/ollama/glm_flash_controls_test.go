package ollama

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

func TestGLMFlashOllamaUsesNativeEffortLevels(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	model := "glm-5.3-flash:cloud"
	provider := New("fixture", "http://localhost:11434", "", nil)
	for level, want := range map[modelcall.ThinkLevel]string{modelcall.ThinkLow: "low", modelcall.ThinkMedium: "low", modelcall.ThinkHigh: "high"} {
		if got := provider.resolveThink(modelcall.CompletionRequest{Think: level}, model); got != want {
			t.Fatalf("Ollama effort = %v, want %s", got, want)
		}
	}
}
