package contract

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Compatible providers differ only in their stored-key requirement.
func TestBundledOpenAICompatibleRegistry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		id              string
		baseURL         string
		apiKeyEnv       string
		label           string // asserted only when set
		kind            string // asserted only when set
		wantConfigured  bool   // keyless AI providers configure without a stored key
		wantRequiresKey bool
	}{
		{id: "openai-compatible", baseURL: "http://127.0.0.1:8080/v1", apiKeyEnv: "", label: "OpenAI-compatible", kind: "openai-compatible", wantConfigured: true, wantRequiresKey: false},
		{id: "litellm-proxy", baseURL: "http://localhost:4000/v1", apiKeyEnv: "LITELLM_MASTER_KEY", label: "LiteLLM", wantConfigured: false, wantRequiresKey: true},
		{id: "lmstudio", baseURL: "http://localhost:1234/v1", apiKeyEnv: "", wantConfigured: true, wantRequiresKey: false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			p := bundledProvider(t, tc.id)
			if p.BaseURL != tc.baseURL {
				t.Errorf("base_url = %q, want %q", p.BaseURL, tc.baseURL)
			}
			if p.APIKeyEnv != tc.apiKeyEnv {
				t.Errorf("api_key_env = %q, want %q", p.APIKeyEnv, tc.apiKeyEnv)
			}
			if tc.label != "" && p.Label != tc.label {
				t.Errorf("label = %q, want %q", p.Label, tc.label)
			}
			if tc.kind != "" && p.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", p.Kind, tc.kind)
			}

			catalog := liveCatalogForShipIDs(t, tc.id)
			registry, err := llm.NewRegistry(catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
			contractcheck.FailErr(t, "llm.NewRegistry failed", err)
			if got := registry.IsConfigured(tc.id); got != tc.wantConfigured {
				t.Fatalf("IsConfigured = %v, want %v", got, tc.wantConfigured)
			}

			var found bool
			for _, p := range registry.List(t.Context()) {
				if p.ID != tc.id {
					continue
				}
				found = true
				if p.RequiresAPIKey != tc.wantRequiresKey {
					t.Errorf("requires_api_key = %v, want %v", p.RequiresAPIKey, tc.wantRequiresKey)
				}
				if !p.Features.ToolCalls {
					t.Error("must report tool-call capability (OpenAI-compatible driver)")
				}
				break
			}
			if !found {
				t.Fatalf("registry.List missing %q", tc.id)
			}
		})
	}
}
