package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCloudflareWorkersAIRegistryUnconfiguredWithoutKey(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	// Bundled providers come from the binary; the local overlay stays a real path.
	catalog, err := llm.NewProviderCatalogAt(filepath.Join(t.TempDir(), "providers.local.yaml"))
	contractcheck.FailErr(t, "llm.NewProviderCatalogAt failed", err)
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")), nil)
	if err == nil {
		registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	}
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)
	if registry.IsConfigured("cloudflare-workers-ai") {
		t.Fatal("expected cloudflare-workers-ai unconfigured without API key or stored credential")
	}
}

func TestCloudflareWorkersAIReportsToolCallCapability(t *testing.T) {
	t.Parallel()
	catalog := liveCatalogForShipIDs(t, "cloudflare-workers-ai")
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")), nil)
	if err == nil {
		registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	}
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)

	var toolCalls bool
	for _, p := range registry.List(t.Context()) {
		if p.ID == "cloudflare-workers-ai" {
			toolCalls = p.Features.ToolCalls
			break
		}
	}
	if !toolCalls {
		t.Fatal("cloudflare-workers-ai should report tool-call capability")
	}
}
