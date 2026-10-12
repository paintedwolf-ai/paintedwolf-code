package modelcall

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

func TestUnsupportedReasoningOmitsControlsAcrossTransports(t *testing.T) {
	for name, profile := range map[string]providerprofile.Profile{
		"ollama": providerprofile.Ollama(),
		"openai": providerprofile.OpenAI(),
		"anthropic": providerprofile.Anthropic(),
		"vertex": providerprofile.VertexExpress(),
		"together": providerprofile.Together(),
		"openrouter": providerprofile.OpenRouter(),
	} {
		t.Run(name, func(t *testing.T) {
			entry := modelinfo.Entry{ID: "non-reasoner", Capabilities: modelinfo.ModelCapabilities{
				Reasoning: modelinfo.Evidence(modelinfo.CapabilityUnsupported, "provider-discovery"),
			}}
			if got := ResolveModelThinking(profile, entry, entry.ID); got != (ModelThinking{Style: modelinfo.ThinkStyleNone}) {
				t.Fatalf("unsupported model controls: %+v", got)
			}
			if got := ResolveThinkingCapabilities(profile, entry, entry.ID); got.State != "unsupported" {
				t.Fatalf("unsupported settings: %+v", got)
			}
		})
	}
}
