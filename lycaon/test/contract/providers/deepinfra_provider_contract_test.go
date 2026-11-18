package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

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
