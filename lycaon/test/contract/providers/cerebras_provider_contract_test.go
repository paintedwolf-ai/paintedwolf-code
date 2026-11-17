package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCerebrasRegistryUnconfiguredWithoutKey(t *testing.T) {
	t.Setenv("CEREBRAS_API_KEY", "csk-env-ignored")
	catalog := liveCatalogForShipIDs(t, "cerebras")
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")), nil)
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
