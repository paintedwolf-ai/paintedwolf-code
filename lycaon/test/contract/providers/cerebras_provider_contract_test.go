package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestBundledProvidersIncludeCerebras(t *testing.T) {
	t.Parallel()
	cfg, err := llm.LoadProviderConfig()
	contractcheck.FailErr(t, "llm.LoadProviderConfig failed", err)
	var cb *llm.ProviderEntry
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "cerebras" {
			cb = &cfg.Providers[i]
			break
		}
	}
	if cb == nil {
		t.Fatal("bundled providers missing cerebras")
	}
	if cb.Kind != "cerebras" {
		t.Fatalf("kind = %q, want cerebras", cb.Kind)
	}
	if cb.Label != "Cerebras" {
		t.Fatalf("label = %q, want Cerebras", cb.Label)
	}
	wantBase := "https://api.cerebras.ai/v1"
	if cb.BaseURL != wantBase {
		t.Fatalf("base_url = %q, want %q", cb.BaseURL, wantBase)
	}
	if cb.APIKeyEnv != "CEREBRAS_API_KEY" {
		t.Fatalf("api_key_env = %q, want CEREBRAS_API_KEY", cb.APIKeyEnv)
	}
	if cb.ReasoningWire != providerprofile.ReasoningWireContent {
		t.Fatalf("reasoning_wire = %q, want %q", cb.ReasoningWire, providerprofile.ReasoningWireContent)
	}
	if len(cb.Models) != 0 {
		t.Fatalf("ship cerebras models = %d, want 0 (live discovery only)", len(cb.Models))
	}
}

func TestCerebrasRegistryUnconfiguredWithoutKey(t *testing.T) {
	t.Setenv("CEREBRAS_API_KEY", "csk-env-ignored")
	catalog := liveCatalogForShipIDs(t, "cerebras")
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	if err == nil {
		registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	}
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)
	if registry.IsConfigured("cerebras") {
		t.Fatal("expected cerebras unconfigured without stored credential (env ignored)")
	}
	var found bool
	for _, p := range registry.List(t.Context()) {
		if p.ID != "cerebras" {
			continue
		}
		found = true
		if !p.RequiresAPIKey {
			t.Error("cerebras must require an API key")
		}
		if !p.Features.ToolCalls {
			t.Error("cerebras must report tool-call capability")
		}
		if !p.Features.Thinking {
			t.Error("cerebras must report thinking capability")
		}
		break
	}
	if !found {
		t.Fatal("registry.List missing cerebras")
	}
}
