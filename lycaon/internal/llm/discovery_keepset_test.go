package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

func TestDiscoveryKeepSetCoversKnownStrategies(t *testing.T) {
	want := []providerprofile.DiscoveryStrategy{
		providerprofile.DiscoveryConfigured,
		providerprofile.DiscoveryNone,
		providerprofile.DiscoveryOllama,
		providerprofile.DiscoveryLMStudio,
		providerprofile.DiscoveryOMLX,
		providerprofile.DiscoveryLiteLLM,
		providerprofile.DiscoveryGemini,
		providerprofile.DiscoveryVertexExpress,
		providerprofile.DiscoveryFireworks,
		providerprofile.DiscoveryCloudflareWorkersAI,
		providerprofile.DiscoveryAnthropic,
		providerprofile.DiscoveryAzure,
		providerprofile.DiscoveryBedrock,
		providerprofile.DiscoveryVertex,
	}
	for _, s := range want {
		gap, ok := DiscoveryKeepSetGap(s)
		if !ok || gap == "" {
			t.Fatalf("keep-set missing %q", s)
		}
	}
	if len(DiscoveryKeepSetSnapshot()) != len(want) {
		t.Fatalf("discoveryKeepSet size = %d, want %d", len(DiscoveryKeepSetSnapshot()), len(want))
	}
}
