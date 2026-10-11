package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Ship YAML carries kind templates only — models: [] on every provider.
// Usable models come from live discovery once a provider is configured.
func TestBundledProvidersIncludeCloudflareWorkersAI(t *testing.T) {
	t.Parallel()
	cfg, err := llm.LoadProviderConfig()
	contractcheck.FailErr(t, "llm.LoadProviderConfig failed", err)
	var cf *llm.ProviderEntry
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "cloudflare-workers-ai" {
			cf = &cfg.Providers[i]
			break
		}
	}
	if cf == nil {
		t.Fatal("bundled providers missing cloudflare-workers-ai")
	}
	if cf.Label != "Cloudflare Workers AI" {
		t.Fatalf("label = %q, want Cloudflare Workers AI", cf.Label)
	}
	if cf.Kind != "cloudflare-workers-ai" {
		t.Fatalf("kind = %q, want cloudflare-workers-ai", cf.Kind)
	}
	wantBase := "https://api.cloudflare.com/client/v4/accounts/YOUR_ACCOUNT_ID/ai/v1"
	if cf.BaseURL != wantBase {
		t.Fatalf("base_url = %q, want %q", cf.BaseURL, wantBase)
	}
	if cf.APIKeyEnv != "CLOUDFLARE_API_TOKEN" {
		t.Fatalf("api_key_env = %q", cf.APIKeyEnv)
	}
	if len(cf.Models) != 0 {
		t.Fatalf("ship cloudflare-workers-ai models = %d, want 0 (live discovery only)", len(cf.Models))
	}
}

func TestCloudflareWorkersAIRegistryUnconfiguredWithoutKey(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	// Bundled providers come from the binary; the local overlay stays a real path.
	catalog, err := llm.NewProviderCatalogAt(filepath.Join(t.TempDir(), "providers.local.yaml"))
	contractcheck.FailErr(t, "llm.NewProviderCatalogAt failed", err)
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)
	if registry.IsConfigured("cloudflare-workers-ai") {
		t.Fatal("expected cloudflare-workers-ai unconfigured without API key or stored credential")
	}
}

func TestCloudflareWorkersAIReportsToolCallCapability(t *testing.T) {
	t.Parallel()
	catalog := liveCatalogForShipIDs(t, "cloudflare-workers-ai")
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
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
