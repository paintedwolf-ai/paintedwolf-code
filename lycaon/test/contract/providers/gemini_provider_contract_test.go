package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

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
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")), nil)
	if err == nil {
		registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	}
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)
	if registry.IsConfigured("gemini") {
		t.Fatal("expected gemini unconfigured without stored credential (env ignored)")
	}
}
