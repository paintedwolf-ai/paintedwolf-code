package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

func TestMergeBedrockInferenceProfilesExposesOnlyCallableProfileIDs(t *testing.T) {
	feed := []modelinfo.Entry{
		{ID: "anthropic.claude-3-5-sonnet", MaxTokens: 8192, Capabilities: modelinfo.ModelCapabilities{
			Chat:  modelinfo.Evidence(modelinfo.CapabilitySupported, "models.dev"),
			Tools: modelinfo.Evidence(modelinfo.CapabilitySupported, "models.dev"),
		}},
		{ID: "unavailable.foundation-model", Capabilities: modelinfo.ModelCapabilities{
			Chat: modelinfo.Evidence(modelinfo.CapabilitySupported, "models.dev"),
		}},
	}
	discovered := []modelinfo.Entry{{
		ID: "us.anthropic.claude-3-5-sonnet", PricedAs: "anthropic.claude-3-5-sonnet",
	}}
	got := mergeBedrockInferenceProfiles(nil, discovered, feed)
	if len(got) != 1 || got[0].ID != "us.anthropic.claude-3-5-sonnet" {
		t.Fatalf("models = %+v", got)
	}
	if got[0].PricedAs != "anthropic.claude-3-5-sonnet" ||
		!modelinfo.Supported(got[0].EffectiveCapabilities().Tools) {
		t.Fatalf("profile metadata = %+v", got[0])
	}
	if got[0].MaxTokens != 8192 {
		t.Fatalf("MaxTokens = %d, want the routed foundation model's output ceiling", got[0].MaxTokens)
	}
}

func TestMergeBedrockInferenceProfilesDropsUnverifiedFoundationModel(t *testing.T) {
	got := mergeBedrockInferenceProfiles(nil, []modelinfo.Entry{{
		ID: "custom-profile", PricedAs: "unknown.foundation-model",
	}}, nil)
	if len(got) != 0 {
		t.Fatalf("unverified profiles = %+v", got)
	}
}
