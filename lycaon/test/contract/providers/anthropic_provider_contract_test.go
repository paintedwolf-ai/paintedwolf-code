package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func bundledProvider(t *testing.T, id string) llm.ProviderEntry {
	t.Helper()
	cfg, err := llm.LoadProviderConfig()
	contractcheck.FailErr(t, "llm.LoadProviderConfig failed", err)
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == id {
			return cfg.Providers[i]
		}
	}
	t.Fatalf("bundled providers missing %q", id)
	return llm.ProviderEntry{}
}

func TestAnthropicRegistryConfiguredWithKey(t *testing.T) {
	t.Parallel()
	catalog := liveCatalogForShipIDs(t, "anthropic")
	creds := providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age"))
	if err := creds.Set("anthropic", "sk-ant-test"); err != nil {
		contractcheck.FailErr(t, "creds.Set failed", err)
	}
	registry, err := llm.NewRegistry(t.Context(), catalog, creds, nil)
	if err == nil {
		registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	}
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)
	if !registry.IsConfigured("anthropic") {
		t.Fatal("expected anthropic configured with stored credential")
	}
	// Get returns the driver behind the host's decorators, so it is named by the
	// wire it reports rather than by its Go type.
	p, err := registry.Get("anthropic")
	contractcheck.FailErr(t, "registry.Get(anthropic) failed", err)
	profile := p.Profile()
	if profile.Discovery != providerprofile.DiscoveryAnthropic || profile.PromptCache.Mode != providerprofile.PromptCacheExplicitBreakpoints {
		t.Fatalf("anthropic provider profile = %+v, want the Anthropic driver", profile)
	}
}
