package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// Ship YAML carries kind templates only — models: [] on every provider.
// Usable models come from live discovery once a provider is configured.
func TestBundledProvidersIncludeFireworks(t *testing.T) {
	t.Parallel()
	cfg, err := llm.LoadProviderConfig()
	contractcheck.FailErr(t, "llm.LoadProviderConfig failed", err)
	var fw *llm.ProviderEntry
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "fireworks" {
			fw = &cfg.Providers[i]
			break
		}
	}
	if fw == nil {
		t.Fatal("bundled providers missing fireworks")
	}
	wantBase := "https://api.fireworks.ai/inference/v1"
	if fw.BaseURL != wantBase {
		t.Fatalf("base_url = %q, want %q", fw.BaseURL, wantBase)
	}
	if fw.APIKeyEnv != "FIREWORKS_API_KEY" {
		t.Fatalf("api_key_env = %q", fw.APIKeyEnv)
	}
	if len(fw.Models) != 0 {
		t.Fatalf("ship fireworks models = %d, want 0 (live discovery only)", len(fw.Models))
	}
}

func TestFireworksRegistryUnconfiguredWithoutKey(t *testing.T) {
	t.Setenv("FIREWORKS_API_KEY", "fw-env-ignored")
	catalog, err := llm.NewProviderCatalogAt(filepath.Join(t.TempDir(), "providers.local.yaml"))
	contractcheck.FailErr(t, "llm.NewProviderCatalogAt failed", err)
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)
	if registry.IsConfigured("fireworks") {
		t.Fatal("expected fireworks unconfigured without stored credential (env ignored)")
	}
}

// TestFireworksExamplePolicyValid checks the fixture policy's own model refs
// (Fireworks provider, correct model-id shape) — the ship catalog carries no
// pricing hints to validate against, since usable models come from live
// discovery once fireworks is configured.
func TestFireworksExamplePolicyValid(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	policyPath := filepath.Join(root, "lycaon", "config", "fixtures", "model-policy.fireworks.example.yaml")
	data, err := os.ReadFile(policyPath)
	contractcheck.FailErr(t, "read file", err)
	var policy llm.ModelPolicy
	if err := yaml.Unmarshal(data, &policy); err != nil {
		contractcheck.FailErr(t, "unmarshal YAML document", err)
	}
	refs := []llm.ModelRef{policy.Coordinator, policy.Lite}
	refs = append(refs, policy.AgentPool.Models...)
	if len(refs) == 0 {
		t.Fatal("example policy has no model refs")
	}
	for _, ref := range refs {
		if ref.ProviderID != "fireworks" {
			t.Fatalf("example policy ref %q not fireworks provider", ref.ProviderID)
		}
		if !strings.HasPrefix(ref.Model, "accounts/fireworks/models/") {
			t.Fatalf("example policy model %q missing fireworks path prefix", ref.Model)
		}
	}
}
