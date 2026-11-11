package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestBundledProvidersIncludeDeepInfra(t *testing.T) {
	t.Parallel()
	cfg, err := llm.LoadProviderConfig()
	contractcheck.FailErr(t, "llm.LoadProviderConfig failed", err)
	var di *llm.ProviderEntry
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "deepinfra" {
			di = &cfg.Providers[i]
			break
		}
	}
	if di == nil {
		t.Fatal("bundled providers missing deepinfra")
	}
	if di.Kind != "deepinfra" {
		t.Fatalf("kind = %q, want deepinfra", di.Kind)
	}
	if di.Label != "DeepInfra" {
		t.Fatalf("label = %q, want DeepInfra", di.Label)
	}
	wantBase := "https://api.deepinfra.com/v1/openai"
	if di.BaseURL != wantBase {
		t.Fatalf("base_url = %q, want %q", di.BaseURL, wantBase)
	}
	if di.APIKeyEnv != "DEEPINFRA_API_KEY" {
		t.Fatalf("api_key_env = %q, want DEEPINFRA_API_KEY", di.APIKeyEnv)
	}
	if di.ReasoningWire != providerprofile.ReasoningWireContent {
		t.Fatalf("reasoning_wire = %q, want %q", di.ReasoningWire, providerprofile.ReasoningWireContent)
	}
	if len(di.Models) != 0 {
		t.Fatalf("ship deepinfra models = %d, want 0 (live discovery only)", len(di.Models))
	}
}

func TestDeepInfraRegistryUnconfiguredWithoutKey(t *testing.T) {
	t.Setenv("DEEPINFRA_API_KEY", "di-env-ignored")
	catalog := liveCatalogForShipIDs(t, "deepinfra")
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")), nil)
	if err == nil {
		registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	}
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)
	if registry.IsConfigured("deepinfra") {
		t.Fatal("expected deepinfra unconfigured without stored credential (env ignored)")
	}
	var found bool
	for _, p := range registry.List(t.Context()) {
		if p.ID != "deepinfra" {
			continue
		}
		found = true
		if !p.RequiresAPIKey {
			t.Error("deepinfra must require an API key")
		}
		if !p.Features.ToolCalls {
			t.Error("deepinfra must report tool-call capability")
		}
		if !p.Features.Thinking {
			t.Error("deepinfra must report thinking capability")
		}
		break
	}
	if !found {
		t.Fatal("registry.List missing deepinfra")
	}
}
