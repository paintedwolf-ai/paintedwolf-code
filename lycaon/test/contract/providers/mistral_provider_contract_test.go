package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestBundledProvidersIncludeMistral(t *testing.T) {
	t.Parallel()
	cfg, err := llm.LoadProviderConfig()
	contractcheck.FailErr(t, "llm.LoadProviderConfig failed", err)
	var mistral *llm.ProviderEntry
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "mistral" {
			mistral = &cfg.Providers[i]
			break
		}
	}
	if mistral == nil {
		t.Fatal("bundled providers missing mistral")
	}
	if mistral.Label != "Mistral" {
		t.Fatalf("label = %q, want Mistral", mistral.Label)
	}
	if mistral.Kind != "mistral" {
		t.Fatalf("kind = %q, want mistral", mistral.Kind)
	}
	wantBase := "https://api.mistral.ai/v1"
	if mistral.BaseURL != wantBase {
		t.Fatalf("base_url = %q, want %q", mistral.BaseURL, wantBase)
	}
	if mistral.APIKeyEnv != "MISTRAL_API_KEY" {
		t.Fatalf("api_key_env = %q, want MISTRAL_API_KEY", mistral.APIKeyEnv)
	}
	if len(mistral.Models) != 0 {
		t.Fatalf("ship mistral models = %d, want 0 (live discovery only)", len(mistral.Models))
	}
}

func TestMistralRegistryUnconfiguredWithoutKey(t *testing.T) {
	t.Setenv("MISTRAL_API_KEY", "")
	catalog, err := llm.NewProviderCatalogAt(filepath.Join(t.TempDir(), "providers.local.yaml"))
	contractcheck.FailErr(t, "llm.NewProviderCatalogAt failed", err)
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	if err == nil {
		registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	}
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)
	if registry.IsConfigured("mistral") {
		t.Fatal("expected mistral unconfigured without stored credential")
	}
}

func TestMistralReportsToolCallCapability(t *testing.T) {
	t.Parallel()
	catalog := liveCatalogForShipIDs(t, "mistral")
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	if err == nil {
		registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	}
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)

	var toolCalls bool
	for _, p := range registry.List(t.Context()) {
		if p.ID == "mistral" {
			toolCalls = p.Features.ToolCalls
			break
		}
	}
	if !toolCalls {
		t.Fatal("mistral should report tool-call capability")
	}
}
