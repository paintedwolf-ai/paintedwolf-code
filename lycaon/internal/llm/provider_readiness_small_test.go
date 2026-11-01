package llm

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerauth"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
)

// MinimalShipHTTPRetryYAML is an indented http_retry block for synthetic ship
// fixtures. Production providers.yaml must declare full per-kind policy.
const MinimalShipHTTPRetryYAML = `    http_retry:
      max_retries: 1
      max_wait_ms: 1000
      backoff_ms: [1]
      statuses: [429]
      wait_headers: [Retry-After]
`

func TestPlatformsSupported(t *testing.T) {
	if !PlatformsSupported(nil, PlatformLinux) {
		t.Fatal("empty platforms should allow all hosts")
	}
	if !PlatformsSupported([]string{PlatformmacOS}, PlatformmacOS) {
		t.Fatal("macos should match")
	}
	if PlatformsSupported([]string{PlatformmacOS}, PlatformLinux) {
		t.Fatal("macos-only should reject linux")
	}
}

func TestHostProductPlatformOverride(t *testing.T) {
	t.Setenv("LYCAON_TEST", "1")
	t.Cleanup(SetHostProductPlatformForTest(PlatformWindows))
	if HostProductPlatform() != PlatformWindows {
		t.Fatalf("HostProductPlatform = %q", HostProductPlatform())
	}
	if !PlatformsSupportedHere([]string{PlatformWindows}) {
		t.Fatal("expected windows supported here")
	}
}

func TestBedrockReadinessRequiresRegionAndUsableAuthentication(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", configDir+"/config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", configDir+"/credentials")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_REGION", "us-west-2")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")

	if configuration, authentication := providerauth.BedrockReadiness(context.Background(), "", ""); configuration != "valid" || authentication != "unverified" {
		t.Fatalf("ambient readiness = %s/%s, want valid/unverified", configuration, authentication)
	}
	if configuration, authentication := providerauth.BedrockReadiness(context.Background(), "us-east-1", "bedrock-api-key"); configuration != "valid" || authentication != "unverified" {
		t.Fatalf("stored-key readiness = %s/%s, want valid/unverified", configuration, authentication)
	}

	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	if configuration, authentication := providerauth.BedrockReadiness(context.Background(), "", "bedrock-api-key"); configuration != "invalid" || authentication != "missing" {
		t.Fatalf("region-less readiness = %s/%s, want invalid/missing", configuration, authentication)
	}
}

// Factory-built profiles select the completion-cap field for each provider kind.
func TestCompletionCapFieldByProviderKind(t *testing.T) {
	cases := []struct {
		kind      string
		wantField string
		deadField string
	}{
		{kind: "openai", wantField: "max_completion_tokens", deadField: "max_tokens"},
		{kind: "openai-compatible", wantField: "max_tokens", deadField: "max_completion_tokens"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			built, err := driverFactories.Build(context.Background(), tc.kind, providerBuild{
				entry: CatalogEntry{
					ID:      tc.kind,
					Kind:    tc.kind,
					BaseURL: "http://localhost/v1",
					Models:  []modelinfo.Entry{{ID: "m", MaxTokens: 8192}},
				},
				apiKey: "k",
			})
			testutil.FailErr(t, "build provider", err)
			provider, ok := built.(*openaicompat.Provider)
			if !ok {
				t.Fatalf("kind %q built %T, want *OpenAIProvider", tc.kind, built)
			}

			body, err := provider.Prepare(modelcall.CompletionRequest{Model: "m"}, false)
			testutil.FailErr(t, "encode request", err)
			var wire map[string]any
			testutil.FailErr(t, "decode request", json.Unmarshal(body, &wire))

			if wire[tc.wantField] != float64(8192) {
				t.Fatalf("%s = %v, want 8192; body=%s", tc.wantField, wire[tc.wantField], body)
			}
			if _, exists := wire[tc.deadField]; exists {
				t.Fatalf("kind %q emitted %s: %s", tc.kind, tc.deadField, body)
			}
		})
	}
}

func TestHydrateProviderModelsDoesNotMutatePublishedProvider(t *testing.T) {
	original := openaicompat.New("openai", "https://example.invalid/v1", "key", []modelinfo.Entry{{ID: "old"}})
	hydrated := hydrateProviderModels(original, []modelinfo.Entry{{ID: "new"}})

	if got := original.Models(); len(got) != 1 || got[0].ID != "old" {
		t.Fatalf("published provider mutated in place: %+v", got)
	}
	if hydrated == original {
		t.Fatal("hydration returned the published provider object")
	}
	if got := hydrated.Models(); len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("hydrated provider models = %+v", got)
	}
}

func TestHydrateProviderModelsCanPublishAnEmptyCatalog(t *testing.T) {
	original := openaicompat.New("openai", "https://example.invalid/v1", "key", []modelinfo.Entry{{ID: "stale"}})
	hydrated := hydrateProviderModels(original, nil)
	if got := hydrated.Models(); len(got) != 0 {
		t.Fatalf("empty refresh retained stale models: %+v", got)
	}
	if got := original.Models(); len(got) != 1 || got[0].ID != "stale" {
		t.Fatalf("empty refresh mutated published provider: %+v", got)
	}
}

func TestHydrateProviderModelsInstallsNativeDiscoveryCapabilities(t *testing.T) {
	original := ollamaprovider.New("ollama", "http://localhost:11434/v1", "", []modelinfo.Entry{{ID: "vision-model"}})
	hydrated, ok := hydrateProviderModels(original, []modelinfo.Entry{{
		ID:           "vision-model",
		Capabilities: modelinfo.ModelCapabilities{Vision: modelinfo.Evidence(modelinfo.CapabilitySupported, "ollama-show")},
	}}).(*ollamaprovider.Provider)
	if !ok {
		t.Fatalf("hydrated provider type = %T", hydrated)
	}
	entry, ok := hydrated.ModelEntry("vision-model")
	if !ok || !modelinfo.Supported(entry.EffectiveCapabilities().Vision) {
		t.Fatalf("hydrated entry = %+v, %v", entry, ok)
	}
	originalEntry, _ := original.ModelEntry("vision-model")
	if modelinfo.Supported(originalEntry.EffectiveCapabilities().Vision) {
		t.Fatal("native hydration mutated the published provider")
	}
}

func TestRegistryTestConnectivityAzureProbesDataPlaneKey(t *testing.T) {
	const validKey = "valid-azure-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("api-key") != validKey {
			http.Error(w, "invalid key", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	t.Cleanup(srv.Close)

	newRegistry := func(t *testing.T, key string) *Registry {
		t.Helper()
		entry := CatalogEntry{
			ID:             "azure",
			Kind:           "azure",
			BaseURL:        srv.URL,
			APIKeyEnv:      "TEST_AZURE_KEY",
			RequiresAPIKey: true,
			Models:         []modelinfo.Entry{{ID: "customer-deployment", PricedAs: "gpt-4o"}},
		}
		catalog := mustTestProviderCatalog(t, entry)
		credentials := providercredentials.NewAt(t.TempDir() + "/credential-vault.age")
		testutil.FailErr(t, "store Azure key", credentials.Set("azure", key))
		registry, err := NewRegistry(t.Context(), catalog, credentials)
		if err == nil {
			registry.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
		}
		testutil.FailErr(t, "create registry", err)
		registry.discoveryClient = srv.Client()
		return registry
	}

	count, err := newRegistry(t, validKey).TestConnectivity(t.Context(), "azure")
	testutil.FailErr(t, "test valid Azure connection", err)
	if count != 1 {
		t.Fatalf("models = %d, want the configured deployment", count)
	}
	if _, err := newRegistry(t, "bogus").TestConnectivity(t.Context(), "azure"); err == nil {
		t.Fatal("bogus Azure key passed TestConnectivity")
	}
}

func TestProviderKindProtocolResolution(t *testing.T) {
	cases := []struct {
		kind     string
		protocol ProviderProtocol
	}{
		{kind: "azure", protocol: ProtocolOpenAICompat},
		{kind: "vertex", protocol: ProtocolOpenAICompat},
		{kind: "vertex-express", protocol: ProtocolGeminiNative},
		{kind: "bedrock", protocol: ProtocolBedrock},
		{kind: "anthropic", protocol: ProtocolAnthropic},
	}
	for _, tc := range cases {
		resolved := ResolveProvider(CatalogEntry{Kind: tc.kind}, "")
		if resolved.Spec.Protocol != tc.protocol {
			t.Errorf("%s protocol = %q, want %q", tc.kind, resolved.Spec.Protocol, tc.protocol)
		}
	}
}

func TestAzureV1BaseIsIdempotent(t *testing.T) {
	want := "https://acme.openai.azure.com/openai/v1"
	if got := azureV1Base(want); got != want {
		t.Fatalf("azureV1Base(%q) = %q", want, got)
	}
}

func TestAzureConfigurationAcceptsOnlyResourceRoot(t *testing.T) {
	if err := ValidateProviderProtocolBase("azure", "https://acme.openai.azure.com"); err != nil {
		t.Fatalf("resource root rejected: %v", err)
	}
	if err := ValidateProviderProtocolBase("azure", "https://acme.openai.azure.com/openai/v1"); err == nil {
		t.Fatal("pre-resolved Azure path was accepted as user configuration")
	}
}

func TestValidRegionIdentifier(t *testing.T) {
	for _, region := range []string{"us-east-1", "europe-west1"} {
		if !providerauth.ValidRegionIdentifier(region) {
			t.Errorf("valid region %q rejected", region)
		}
	}
	for _, region := range []string{"", "US-EAST-1", "-west", "west-", "west/east"} {
		if providerauth.ValidRegionIdentifier(region) {
			t.Errorf("invalid region %q accepted", region)
		}
	}
}

// Explicit driver kinds declare protocol and base URL behavior.
func TestEveryDriverKindHasAProtocolSpec(t *testing.T) {
	for _, kind := range driverFactories.Kinds() {
		if _, ok := providerKindSpecs[kind]; !ok {
			t.Errorf("providerKindSpecs is missing %q — declare its protocol and base URL", kind)
		}
	}
}

func TestNewProviderForEntryAttachesHTTPRetry(t *testing.T) {
	policy := providerretry.ProviderHTTPRetry{
		MaxRetries:  7,
		MaxWaitMs:   1000,
		BackoffMs:   []int{1, 1, 1, 1, 1, 1, 1},
		Statuses:    []int{429},
		WaitHeaders: []string{"Retry-After"},
	}
	for _, kind := range []string{"openai", "anthropic", "ollama", "fireworks"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Model discovery and context probes share the server; count completion attempts only.
				path := r.URL.Path
				if strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/messages") || strings.HasSuffix(path, "/api/chat") {
					calls.Add(1)
				}
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
			}))
			defer server.Close()
			entry := CatalogEntry{ID: "p", Kind: kind, BaseURL: server.URL + "/v1", HTTPRetry: policy, Models: []modelinfo.Entry{{ID: "model", ContextLength: 32768, ThinkStyle: "none"}}}
			reg, err := NewRegistry(t.Context(), mustTestProviderCatalog(t, entry), providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
			if err == nil {
				reg.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
			}
			if err != nil {
				t.Fatalf("NewRegistry: %v", err)
			}
			p, err := newProviderForEntry(context.Background(), entry, "key", reg.providerRetryPolicy(entry), reg.cloudflareUsage, reg.discoveryClient)
			if err != nil {
				t.Fatalf("newProviderForEntry: %v", err)
			}
			_, err = p.Complete(t.Context(), modelcall.CompletionRequest{Model: "model", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}}})
			limited, ok := failure.AsProviderRateLimited(err)
			if !ok || limited.Attempts != 8 || calls.Load() != 8 {
				t.Fatalf("factory retry policy: requests=%d error=%v, want eight rate-limited attempts", calls.Load(), err)
			}
		})
	}

}
