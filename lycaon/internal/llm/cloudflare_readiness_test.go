package llm

import (
	"path/filepath"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCloudflareReadinessRequiresAccountAndKey(t *testing.T) {
	for _, tc := range []struct {
		name          string
		account       string
		key           string
		configuration string
		configured    bool
	}{
		{"placeholder with key", "YOUR_ACCOUNT_ID", "test-token", "invalid", false},
		{"lowercase placeholder", "your_account_id", "test-token", "invalid", false},
		{"empty account", "", "test-token", "invalid", false},
		{"account without key", "account-1", "", "valid", false},
		{"account with key", "account-1", "test-token", "valid", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := &Registry{}
			state, configured := registry.entryReadiness(t.Context(), CatalogEntry{
				Kind: "cloudflare-workers-ai", RequiresAPIKey: true,
				BaseURL: "https://api.cloudflare.com/client/v4/accounts/" + tc.account + "/ai/v1",
			}, tc.key)
			if state.Configuration != tc.configuration || configured != tc.configured {
				t.Fatalf("readiness = %+v, configured = %v; want configuration %s, configured %v",
					state, configured, tc.configuration, tc.configured)
			}
		})
	}
}

func TestCloudflarePlaceholderWithSavedKeyIsNotAssignable(t *testing.T) {
	catalog := mustCatalogCloneShipToLocal(t, `providers:
  - id: cloudflare-workers-ai-1
    kind: cloudflare-workers-ai
    base_url: https://api.cloudflare.com/client/v4/accounts/YOUR_ACCOUNT_ID/ai/v1
    requires_api_key: true
    models:
      - id: "@cf/meta/llama-3.1-8b-instruct"
`+MinimalShipHTTPRetryYAML)
	creds := providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age"))
	testutil.FailErr(t, "save credential", creds.Set("cloudflare-workers-ai-1", "test-token"))
	registry, err := NewRegistry(t.Context(), catalog, creds)
	testutil.FailErr(t, "create registry", err)
	providers := registry.List(t.Context())
	if len(providers) != 1 {
		t.Fatalf("provider count = %d, want 1", len(providers))
	}
	provider := providers[0]
	if provider.Configured || provider.ReadyToAssign {
		t.Fatalf("placeholder provider is ready: configured=%v, assignable=%v",
			provider.Configured, provider.ReadyToAssign)
	}
	if !provider.CredentialPresent {
		t.Fatal("saved credential must remain visible while account setup is incomplete")
	}
}
