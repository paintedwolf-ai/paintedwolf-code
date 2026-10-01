package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/providerauth"
)

func TestProviderKindProtocolResolution(t *testing.T) {
	cases := []struct {
		kind     string
		protocol ProviderProtocol
	}{
		{kind: "azure", protocol: ProtocolOpenAICompat},
		{kind: "vertex", protocol: ProtocolOpenAICompat},
		{kind: "vertex-express", protocol: ProtocolGeminiNative},
		{kind: "bedrock", protocol: ProtocolBedrock},
		{kind: "anthropic", protocol: ProtocolAnthropic},
	}
	for _, tc := range cases {
		resolved := ResolveProvider(CatalogEntry{Kind: tc.kind}, "")
		if resolved.Spec.Protocol != tc.protocol {
			t.Errorf("%s protocol = %q, want %q", tc.kind, resolved.Spec.Protocol, tc.protocol)
		}
	}
}

func TestAzureV1BaseIsIdempotent(t *testing.T) {
	want := "https://acme.openai.azure.com/openai/v1"
	if got := azureV1Base(want); got != want {
		t.Fatalf("azureV1Base(%q) = %q", want, got)
	}
}

func TestAzureConfigurationAcceptsOnlyResourceRoot(t *testing.T) {
	if err := ValidateProviderProtocolBase("azure", "https://acme.openai.azure.com"); err != nil {
		t.Fatalf("resource root rejected: %v", err)
	}
	if err := ValidateProviderProtocolBase("azure", "https://acme.openai.azure.com/openai/v1"); err == nil {
		t.Fatal("pre-resolved Azure path was accepted as user configuration")
	}
}

func TestValidRegionIdentifier(t *testing.T) {
	for _, region := range []string{"us-east-1", "europe-west1"} {
		if !providerauth.ValidRegionIdentifier(region) {
			t.Errorf("valid region %q rejected", region)
		}
	}
	for _, region := range []string{"", "US-EAST-1", "-west", "west-", "west/east"} {
		if providerauth.ValidRegionIdentifier(region) {
			t.Errorf("invalid region %q accepted", region)
		}
	}
}

// Explicit driver kinds declare protocol and base URL behavior.
func TestEveryDriverKindHasAProtocolSpec(t *testing.T) {
	for _, kind := range driverFactories.Kinds() {
		if _, ok := providerKindSpecs[kind]; !ok {
			t.Errorf("providerKindSpecs is missing %q — declare its protocol and base URL", kind)
		}
	}
}
