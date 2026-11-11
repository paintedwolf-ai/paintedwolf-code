package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/modelfeed"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestBundledScalewayProviderTemplateAndExclusions(t *testing.T) {
	t.Parallel()
	scw := bundledProvider(t, "scaleway")
	if scw.BaseURL != "https://api.scaleway.ai/v1" {
		t.Fatalf("base_url = %q, want https://api.scaleway.ai/v1", scw.BaseURL)
	}
	if scw.APIKeyEnv != "SCW_SECRET_KEY" {
		t.Fatalf("api_key_env = %q, want SCW_SECRET_KEY", scw.APIKeyEnv)
	}
	if scw.ReasoningWire != providerprofile.ReasoningWireContent {
		t.Fatalf("reasoning_wire = %q, want %q", scw.ReasoningWire, providerprofile.ReasoningWireContent)
	}
	if scw.HTTPRetryProfile != "hosted-standard" {
		t.Fatalf("http_retry_profile = %q, want hosted-standard", scw.HTTPRetryProfile)
	}
	if scw.PromptCacheProfile != "implicit" || scw.PromptCache.Mode != providerprofile.PromptCacheAutomaticPrefix {
		t.Fatalf("prompt_cache = (%q, %+v), want implicit automatic_prefix", scw.PromptCacheProfile, scw.PromptCache)
	}
	if len(scw.Models) != 0 {
		t.Fatalf("ship scaleway models = %d, want 0 (live discovery only)", len(scw.Models))
	}

	exclusions, err := llm.LoadRoleExclusions()
	contractcheck.FailErr(t, "llm.LoadRoleExclusions failed", err)

	chatModel := modelinfo.Entry{
		ID:            "qwen3-235b-a22b-instruct-2507",
		ContextLength: 128000,
		Capabilities: modelinfo.ModelCapabilities{
			Tools: modelinfo.Evidence(modelinfo.CapabilitySupported, "models.dev"),
		},
	}
	for _, role := range []string{llm.PolicySlotCoordinator, llm.PolicySlotAgentPool, llm.PolicySlotLite} {
		if got := llm.ModelRoleEligibility("scaleway", chatModel, role, exclusions); got.State != "eligible" {
			t.Fatalf("chat model role %s = %+v, want eligible", role, got)
		}
	}

	for _, blocked := range []struct {
		id   string
		rule string
	}{
		{id: "qwen3-embedding-8b", rule: "retrieval"},
		{id: "bge-multilingual-gemma2-embedding", rule: "retrieval"},
		{id: "llama-guard-3-8b", rule: "moderation"},
	} {
		entry := modelinfo.Entry{
			ID:            blocked.id,
			ContextLength: 32768,
			Capabilities: modelinfo.ModelCapabilities{
				Tools: modelinfo.Evidence(modelinfo.CapabilitySupported, "fixture"),
			},
		}
		for _, role := range []string{llm.PolicySlotCoordinator, llm.PolicySlotAgentPool, llm.PolicySlotLite} {
			got := llm.ModelRoleEligibility("scaleway", entry, role, exclusions)
			if got.State != "incompatible" || got.RuleID != blocked.rule {
				t.Fatalf("model %s role %s = %+v, want incompatible via %s", blocked.id, role, got, blocked.rule)
			}
		}
	}
}

func TestScalewayModelFeedMappingAndFixture(t *testing.T) {
	t.Parallel()
	feedKey, ok := modelfeed.FeedKeyForKind("scaleway")
	if !ok || feedKey != "scaleway" {
		t.Fatalf("FeedKeyForKind(scaleway) = (%q, %v), want (\"scaleway\", true)", feedKey, ok)
	}
	kind, ok := modelfeed.KindForFeedKey("scaleway")
	if !ok || kind != "scaleway" {
		t.Fatalf("KindForFeedKey(scaleway) = (%q, %v), want (\"scaleway\", true)", kind, ok)
	}

	root := contractcheck.RepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "lycaon", "internal", "modelfeed", "testdata", "models_dev_fixture.json"))
	contractcheck.FailErr(t, "read models_dev_fixture.json", err)
	doc, err := modelfeed.ParseDocument(raw)
	contractcheck.FailErr(t, "modelfeed.ParseDocument", err)

	eligible := doc.EligibleModels("scaleway")
	if len(eligible) == 0 {
		t.Fatal("expected eligible scaleway models in fixture")
	}
	seenChat := false
	for _, m := range eligible {
		if m.ID == "qwen3-235b-a22b-instruct-2507" {
			seenChat = true
		}
		if m.ID == "qwen3-embedding-8b" {
			t.Fatal("embedding model must not be conversation-eligible")
		}
	}
	if !seenChat {
		t.Fatalf("expected qwen3-235b-a22b-instruct-2507 in eligible models, got %+v", eligible)
	}
}
