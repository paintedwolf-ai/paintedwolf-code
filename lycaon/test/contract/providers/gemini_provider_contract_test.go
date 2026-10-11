package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Bundled providers are templates with discovered models.
func TestBundledProvidersIncludeGemini(t *testing.T) {
	t.Parallel()
	cfg, err := llm.LoadProviderConfig()
	contractcheck.FailErr(t, "llm.LoadProviderConfig failed", err)
	var gemini *llm.ProviderEntry
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "gemini" {
			gemini = &cfg.Providers[i]
			break
		}
	}
	if gemini == nil {
		t.Fatal("bundled providers missing gemini")
	}
	wantBase := "https://generativelanguage.googleapis.com/v1beta/openai"
	if gemini.BaseURL != wantBase {
		t.Fatalf("base_url = %q, want %q", gemini.BaseURL, wantBase)
	}
	if gemini.APIKeyEnv != "GEMINI_API_KEY" {
		t.Fatalf("api_key_env = %q", gemini.APIKeyEnv)
	}
	if gemini.Kind != "gemini" {
		t.Fatalf("kind = %q, want gemini", gemini.Kind)
	}
	if len(gemini.Models) != 0 {
		t.Fatalf("ship gemini models = %d, want 0 (live discovery only)", len(gemini.Models))
	}
}

func TestBundledRoleExclusionsCoverSpecialtyInterfaces(t *testing.T) {
	doc, err := llm.LoadRoleExclusions()
	contractcheck.FailErr(t, "load exclusions", err)
	for _, id := range []string{"gemini-3.1-flash-live-preview", "gemini-3-pro-image"} {
		for _, role := range []string{llm.PolicySlotCoordinator, llm.PolicySlotLite} {
			if doc.Match("gemini", id, role) == nil {
				t.Fatalf("missing specialty restriction for %s/%s", id, role)
			}
		}
	}
}

func TestGeminiRegistryUnconfiguredWithoutKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "env-ignored")
	catalog, err := llm.NewProviderCatalogAt(filepath.Join(t.TempDir(), "providers.local.yaml"))
	contractcheck.FailErr(t, "llm.NewProviderCatalogAt failed", err)
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)
	if registry.IsConfigured("gemini") {
		t.Fatal("expected gemini unconfigured without stored credential (env ignored)")
	}
}
