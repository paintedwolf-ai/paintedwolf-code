package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestScalewayRoleEligibility(t *testing.T) {
	t.Parallel()
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
