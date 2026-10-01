package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Hosted providers use live discovery and require a saved key.
func TestBundledOpenAIStyleProviders(t *testing.T) {

	for _, tc := range []struct {
		id        string
		baseURL   string
		apiKeyEnv string
		label     string
		kind      string
	}{
		{id: "openai", baseURL: "https://api.openai.com/v1", apiKeyEnv: "OPENAI_API_KEY", label: "OpenAI", kind: "openai"},
		{id: "openrouter", baseURL: "https://openrouter.ai/api/v1", apiKeyEnv: "OPENROUTER_API_KEY", label: "OpenRouter", kind: "openrouter"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			p := bundledProvider(t, tc.id)
			if p.BaseURL != tc.baseURL {
				t.Errorf("base_url = %q, want %q", p.BaseURL, tc.baseURL)
			}
			if p.APIKeyEnv != tc.apiKeyEnv {
				t.Errorf("api_key_env = %q, want %q", p.APIKeyEnv, tc.apiKeyEnv)
			}
			if p.Label != tc.label {
				t.Errorf("label = %q, want %q", p.Label, tc.label)
			}
			if p.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", p.Kind, tc.kind)
			}
			if len(p.Models) != 0 {
				t.Errorf("ship models = %d, want 0 (live discovery only)", len(p.Models))
			}

			// A stored credential is required — an ambient env var must be ignored.
			t.Setenv(tc.apiKeyEnv, "env-ignored")
			catalog, err := llm.NewProviderCatalogAt(filepath.Join(t.TempDir(), "providers.local.yaml"))
			contractcheck.FailErr(t, "llm.NewProviderCatalogAt failed", err)
			registry, err := llm.NewRegistry(catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
			contractcheck.FailErr(t, "llm.NewRegistry failed", err)
			if registry.IsConfigured(tc.id) {
				t.Fatalf("expected %q unconfigured without stored credential (env ignored)", tc.id)
			}
		})
	}
}
