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

// TestBundledProvidersShipKindTemplates locks the ship-catalog shape for the
// first-party hosted providers. Ship YAML carries kind templates only —
// models: [] on every provider; usable models come from live discovery once a
// provider is configured.
func TestBundledProvidersShipKindTemplates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		id          string
		kind        string
		baseURL     string // asserted only when set
		apiKeyEnv   string
		label       string // asserted only when set
		ambientAuth string
	}{
		{id: "anthropic", kind: "anthropic", baseURL: "https://api.anthropic.com/v1", apiKeyEnv: "ANTHROPIC_API_KEY", label: "Anthropic"},
		{id: "together", kind: "together", baseURL: "https://api.together.xyz/v1", apiKeyEnv: "TOGETHER_API_KEY"},
		{id: "azure", kind: "azure", apiKeyEnv: "AZURE_OPENAI_API_KEY"},
		// Bedrock takes a pasteable API key by default and names the AWS SDK
		// chain as the opt-in alternative; Vertex has no pasteable credential
		// covering the same models, so it stays ambient-only.
		{id: "bedrock", kind: "bedrock", apiKeyEnv: "AWS_BEARER_TOKEN_BEDROCK", ambientAuth: "aws-sdk-chain"},
		{id: "vertex", kind: "vertex", apiKeyEnv: "", ambientAuth: "google-adc"},
		// Express mode uses a separate credential shape.
		{id: "vertex-express", kind: "vertex-express", baseURL: "https://aiplatform.googleapis.com/v1", apiKeyEnv: "GOOGLE_API_KEY", ambientAuth: ""},
	} {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			p := bundledProvider(t, tc.id)
			if p.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", p.Kind, tc.kind)
			}
			if tc.baseURL != "" && p.BaseURL != tc.baseURL {
				t.Errorf("base_url = %q, want %q", p.BaseURL, tc.baseURL)
			}
			if p.APIKeyEnv != tc.apiKeyEnv {
				t.Errorf("api_key_env = %q, want %q", p.APIKeyEnv, tc.apiKeyEnv)
			}
			if tc.label != "" && p.Label != tc.label {
				t.Errorf("label = %q, want %q", p.Label, tc.label)
			}
			if p.AmbientAuth != tc.ambientAuth {
				t.Errorf("ambient_auth = %q, want %q", p.AmbientAuth, tc.ambientAuth)
			}
			if len(p.Models) != 0 {
				t.Errorf("ship models = %d, want 0 (live discovery only)", len(p.Models))
			}
		})
	}
}

func TestAnthropicRegistryConfiguredWithKey(t *testing.T) {
	t.Parallel()
	catalog := liveCatalogForShipIDs(t, "anthropic")
	creds := providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age"))
	if err := creds.Set("anthropic", "sk-ant-test"); err != nil {
		contractcheck.FailErr(t, "creds.Set failed", err)
	}
	registry, err := llm.NewRegistry(t.Context(), catalog, creds)
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

func TestAnthropicRegistryUnconfiguredWithoutKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-env-ignored")
	// Bundled providers come from the binary; the local overlay stays a real path.
	catalog, err := llm.NewProviderCatalogAt(filepath.Join(t.TempDir(), "providers.local.yaml"))
	contractcheck.FailErr(t, "llm.NewProviderCatalogAt failed", err)
	registry, err := llm.NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	contractcheck.FailErr(t, "llm.NewRegistry failed", err)
	if registry.IsConfigured("anthropic") {
		t.Fatal("expected anthropic unconfigured without stored credential (env ignored)")
	}
}
