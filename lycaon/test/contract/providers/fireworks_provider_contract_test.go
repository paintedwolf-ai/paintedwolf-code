package contract

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestFireworksRegistryUnconfiguredWithoutKey(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw-env-ignored")
	catalog, err := llm.NewProviderCatalogAt(filepath.Join(t.TempDir(), "providers.local.yaml"))
	contractcheck.FailErr(t, "llm.NewProviderCatalogAt failed", err)
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")), nil)
	if err == nil {
		registry.SetOutboundSecretScreen(llm.NewModelSecretScreen(nil, nil))
	}
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)
	if registry.IsConfigured("fireworks") {
		t.Fatal("expected fireworks unconfigured without stored credential (env ignored)")
	}
}

// The example policy loads through the global policy layer it documents
// copying to, and every slot resolves to Fireworks.
func TestFireworksExamplePolicyValid(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	store, err := llm.NewPolicyStoreAt(filepath.Join(root, "lycaon", "config", "fixtures", "model-policy.fireworks.example.yaml"))
	contractcheck.FailErr(t, "llm.NewPolicyStoreAt failed", err)
	got, err := store.Overlay(llm.SettingsScopeGlobal, "")
	contractcheck.FailErr(t, "store.Overlay failed", err)
	llama := llm.ModelRef{ProviderID: "fireworks", Model: "accounts/fireworks/models/llama-v3p1-8b-instruct"}
	want := llm.ModelPolicy{
		Coordinator: llama,
		Lite:        llama,
		AgentPool: llm.AgentPool{
			Selection: llm.PoolSelectionRoundRobin,
			Models: []llm.ModelRef{
				llama,
				{ProviderID: "fireworks", Model: "accounts/fireworks/models/qwen2p5-coder-32b-instruct"},
				{ProviderID: "fireworks", Model: "accounts/fireworks/models/deepseek-v3p1"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("example policy overlay = %+v, want %+v", got, want)
	}
}
