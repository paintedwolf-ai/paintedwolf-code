package modelcall

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

func TestUnsupportedReasoningOmitsControlsAcrossTransports(t *testing.T) {
	for name, profile := range map[string]providerprofile.Profile{
		"ollama":     providerprofile.Ollama(),
		"openai":     providerprofile.OpenAI(),
		"anthropic":  providerprofile.Anthropic(),
		"vertex":     providerprofile.VertexExpress(),
		"together":   providerprofile.Together(),
		"openrouter": providerprofile.Openrouter(),
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

func TestThinkingEvidencePrecedence(t *testing.T) {
	modelinfo.SetThinkingRules([]modelinfo.ThinkingRule{{Match: []string{"family"}, Style: "effort_levels", AlwaysOn: true,
		Controls: &modelinfo.ThinkingCapabilities{State: "supported", Efforts: []string{"high"}},
	}})
	t.Cleanup(func() { modelinfo.SetThinkingRules(nil) })
	unsupported := modelinfo.Evidence(modelinfo.CapabilityUnsupported, "fixture")
	for _, tc := range []struct {
		name          string
		entry         modelinfo.Entry
		state, source string
		style         modelinfo.ThinkStyle
	}{
		{"missing", modelinfo.Entry{}, "unknown", "", modelinfo.ThinkStyleEffortLevels},
		{"family", modelinfo.Entry{ID: "family"}, "supported", "model-rule", modelinfo.ThinkStyleEffortLevels},
		{"negative evidence", modelinfo.Entry{ID: "family", Capabilities: modelinfo.ModelCapabilities{Reasoning: unsupported}}, "unsupported", "model-capabilities", modelinfo.ThinkStyleNone},
		{"discovered unsupported", modelinfo.Entry{ID: "family", DiscoveredThinking: &modelinfo.ThinkingCapabilities{State: "unsupported"}}, "unsupported", "provider-discovery", modelinfo.ThinkStyleNone},
		{"discovered unknown", modelinfo.Entry{ID: "family", DiscoveredThinking: &modelinfo.ThinkingCapabilities{State: "unknown"}}, "unknown", "provider-discovery", modelinfo.ThinkStyleNone},
		{"configured unsupported", modelinfo.Entry{Thinking: &modelinfo.ThinkingCapabilities{State: "unsupported"}, DiscoveredThinking: &modelinfo.ThinkingCapabilities{State: "supported"}}, "unsupported", "provider-config", modelinfo.ThinkStyleNone},
		{"configured unknown", modelinfo.Entry{ID: "family", Thinking: &modelinfo.ThinkingCapabilities{State: "unknown"}}, "unknown", "provider-config", modelinfo.ThinkStyleNone},
		{"configured supported", modelinfo.Entry{Thinking: &modelinfo.ThinkingCapabilities{State: "supported", Efforts: []string{"high"}}, Capabilities: modelinfo.ModelCapabilities{Reasoning: unsupported}}, "supported", "provider-config", modelinfo.ThinkStyleEffortLevels},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := providerprofile.OpenAI()
			got := ResolveThinkingCapabilities(profile, tc.entry, tc.entry.ID)
			if got.State != tc.state || got.Source != tc.source {
				t.Fatalf("capabilities = %+v, want %s from %s", got, tc.state, tc.source)
			}
			mt := ResolveModelThinking(profile, tc.entry, tc.entry.ID)
			if mt.Style != tc.style || tc.style == modelinfo.ThinkStyleNone && mt.AlwaysOn {
				t.Fatalf("thinking = %+v, want %s", mt, tc.style)
			}
		})
	}
}
