package llm

import (
	"context"
	"testing"
	"time"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

// shippedPromptCache loads the provider kind catalog and installs its model
// rules, as a catalog reload does.
func shippedPromptCache(t *testing.T) map[string]providerprofile.PromptCachePolicy {
	t.Helper()
	cfg, err := LoadProviderConfig()
	if err != nil {
		testutil.FailErr(t, "load provider kind catalog", err)
	}
	providerprofile.SetPromptCacheRules(cfg.ModelPromptCache)
	t.Cleanup(func() { providerprofile.SetPromptCacheRules(nil) })
	out := make(map[string]providerprofile.PromptCachePolicy, len(cfg.Providers))
	for _, p := range cfg.Providers {
		out[p.Kind] = p.PromptCache
	}
	return out
}

func TestShippedPromptCachePolicyPerKind(t *testing.T) {
	policies := shippedPromptCache(t)
	cases := []struct {
		kind      string
		mode      providerprofile.PromptCacheStyle
		coldAfter time.Duration
	}{
		{"anthropic", providerprofile.PromptCacheExplicitBreakpoints, time.Hour},
		{"openai", providerprofile.PromptCacheAutomaticPrefix, 10 * time.Minute},
		{"azure", providerprofile.PromptCacheAutomaticPrefix, 10 * time.Minute},
		{"fireworks", providerprofile.PromptCacheAutomaticPrefix, 6 * time.Minute},
		{"openrouter", providerprofile.PromptCacheAutomaticPrefix, 10 * time.Minute},
		{"gemini", providerprofile.PromptCacheAutomaticPrefix, 0},
		{"together", providerprofile.PromptCacheAutomaticPrefix, 0},
		{"vertex", providerprofile.PromptCacheAutomaticPrefix, 0},
		{"vertex-express", providerprofile.PromptCacheAutomaticPrefix, 0},
		{"cloudflare-workers-ai", providerprofile.PromptCacheAutomaticPrefix, 0},
		{"litellm-proxy", providerprofile.PromptCacheAutomaticPrefix, 0},
		{"bedrock", providerprofile.PromptCacheAutomaticPrefix, 0},
		{"ollama", providerprofile.PromptCacheLocalKV, 0},
		{"lmstudio", providerprofile.PromptCacheLocalKV, 0},
		{"omlx", providerprofile.PromptCacheLocalKV, 0},
		{"openai-compatible", providerprofile.PromptCacheLocalKV, 0},
	}
	if len(cases) != len(policies) {
		t.Fatalf("catalog has %d provider kinds, test covers %d", len(policies), len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			policy, ok := policies[tc.kind]
			if !ok {
				t.Fatalf("no policy for kind %q", tc.kind)
			}
			if policy.Mode != tc.mode {
				t.Fatalf("mode = %q, want %q", policy.Mode, tc.mode)
			}
			if got := policy.StandingColdAfter(); got != tc.coldAfter {
				t.Fatalf("standing cold after = %v, want %v", got, tc.coldAfter)
			}
		})
	}
}

func TestShippedPromptCacheModelRules(t *testing.T) {
	policies := shippedPromptCache(t)
	cases := []struct {
		name, kind, model string
		mode              providerprofile.PromptCacheStyle
		marker            providerprofile.PromptCacheMarker
		retention         string
		standing, history time.Duration
		coldAfter         time.Duration
	}{
		{name: "openai breakpoint line", kind: "openai", model: "gpt-5.6-sol", mode: providerprofile.PromptCacheAutomaticPrefix, marker: providerprofile.PromptCacheMarkerBreakpoint, coldAfter: 30 * time.Minute},
		{name: "openai later major", kind: "openai", model: "gpt-6-astra", mode: providerprofile.PromptCacheAutomaticPrefix, marker: providerprofile.PromptCacheMarkerBreakpoint, coldAfter: 30 * time.Minute},
		{name: "openai extended retention", kind: "openai", model: "gpt-5.5-pro", mode: providerprofile.PromptCacheAutomaticPrefix, retention: "24h", coldAfter: 30 * time.Minute},
		{name: "openai bare gpt-5 exact", kind: "openai", model: "gpt-5", mode: providerprofile.PromptCacheAutomaticPrefix, retention: "24h", coldAfter: 30 * time.Minute},
		{name: "openai gpt-5 mini keeps the profile", kind: "openai", model: "gpt-5-mini", mode: providerprofile.PromptCacheAutomaticPrefix, coldAfter: 10 * time.Minute},
		{name: "azure retention by deployment", kind: "azure", model: "gpt-4.1", mode: providerprofile.PromptCacheAutomaticPrefix, retention: "24h", coldAfter: 30 * time.Minute},
		{name: "azure takes no breakpoint", kind: "azure", model: "gpt-5.6-sol", mode: providerprofile.PromptCacheAutomaticPrefix, coldAfter: 10 * time.Minute},
		{name: "openrouter anthropic", kind: "openrouter", model: "anthropic/claude-sonnet-5", mode: providerprofile.PromptCacheAutomaticPrefix, marker: providerprofile.PromptCacheMarkerCacheControl, coldAfter: 5 * time.Minute},
		{name: "openrouter openai line", kind: "openrouter", model: "openai/gpt-5.6-sol", mode: providerprofile.PromptCacheAutomaticPrefix, marker: providerprofile.PromptCacheMarkerBreakpoint, coldAfter: 10 * time.Minute},
		{name: "openrouter older openai", kind: "openrouter", model: "openai/gpt-5.5", mode: providerprofile.PromptCacheAutomaticPrefix, coldAfter: 10 * time.Minute},
		{name: "openrouter gemini", kind: "openrouter", model: "google/gemini-3-pro", mode: providerprofile.PromptCacheAutomaticPrefix, marker: providerprofile.PromptCacheMarkerCacheControl, coldAfter: 10 * time.Minute},
		{name: "bedrock current claude", kind: "bedrock", model: "us.anthropic.claude-sonnet-5", mode: providerprofile.PromptCacheExplicitBreakpoints, standing: time.Hour, history: 5 * time.Minute, coldAfter: time.Hour},
		{name: "bedrock five-minute claude", kind: "bedrock", model: "anthropic.claude-3-5-sonnet-20241022-v2:0", mode: providerprofile.PromptCacheExplicitBreakpoints, standing: 5 * time.Minute, history: 5 * time.Minute, coldAfter: 5 * time.Minute},
		{name: "bedrock legacy claude", kind: "bedrock", model: "anthropic.claude-3-haiku-20240307-v1:0", mode: providerprofile.PromptCacheAutomaticPrefix},
		{name: "bedrock nova checkpoints without a lifetime", kind: "bedrock", model: "us.amazon.nova-2-lite-v1:0", mode: providerprofile.PromptCacheExplicitBreakpoints, coldAfter: 5 * time.Minute},
		{name: "bedrock other family", kind: "bedrock", model: "us.meta.llama3-3-70b-instruct-v1:0", mode: providerprofile.PromptCacheAutomaticPrefix},
		{name: "litellm reads the catalog", kind: "litellm-proxy", model: "claude", mode: providerprofile.PromptCacheAutomaticPrefix, marker: providerprofile.PromptCacheMarkerCatalog},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := policies[tc.kind].ForModel(tc.model)
			if got.Mode != tc.mode || got.Marker != tc.marker || got.Retention != tc.retention {
				t.Fatalf("policy = mode %q marker %q retention %q, want %q %q %q", got.Mode, got.Marker, got.Retention, tc.mode, tc.marker, tc.retention)
			}
			if got.Lifetime.Standing.Duration() != tc.standing || got.Lifetime.History.Duration() != tc.history {
				t.Fatalf("lifetime = %v/%v, want %v/%v", got.Lifetime.Standing.Duration(), got.Lifetime.History.Duration(), tc.standing, tc.history)
			}
			if cold := got.StandingColdAfter(); cold != tc.coldAfter {
				t.Fatalf("standing cold after = %v, want %v", cold, tc.coldAfter)
			}
		})
	}
}

func TestNewProviderForEntryAttachesItsPromptCachePolicy(t *testing.T) {
	policies := shippedPromptCache(t)
	for _, kind := range []string{"ollama", "anthropic", "fireworks", "gemini", "openrouter", "openai", "bedrock", "cloudflare-workers-ai"} {
		t.Run(kind, func(t *testing.T) {
			entry := CatalogEntry{ID: "p", Kind: kind, BaseURL: "http://localhost:1234/v1", PromptCache: policies[kind]}
			if kind == "cloudflare-workers-ai" {
				entry.BaseURL = "https://api.cloudflare.com/client/v4/accounts/acct-test/ai/v1"
			}
			if kind == "bedrock" {
				entry.BaseURL = "us-east-1"
			}
			reg, err := NewRegistry(
				mustTestProviderCatalog(t, entry),
				providercredentials.NewAt(t.TempDir()+"/credential-vault.age"),
			)
			if err != nil {
				testutil.FailErr(t, "new registry", err)
			}
			p, err := newProviderForEntry(context.Background(), entry, "key", reg.providerRetryPolicy(entry), reg.cloudflareUsage, reg.discoveryClient)
			if err != nil {
				testutil.FailErr(t, "new provider for entry", err)
			}
			if got := p.Profile().PromptCache; got != policies[kind] {
				t.Fatalf("PromptCache = %+v, want %+v", got, policies[kind])
			}
		})
	}
}

func TestUnattachedDriverCachesNothing(t *testing.T) {
	var profile providerprofile.Profile
	if profile.PromptCache.Mode.Caches() || profile.PromptCache.Mode.WireValue() != string(providerprofile.PromptCacheNone) {
		t.Fatalf("zero policy = %q, want none", profile.PromptCache.Mode)
	}
}
