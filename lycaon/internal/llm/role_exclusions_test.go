package llm

import (
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBundledRoleExclusionsAllowGeneralModels(t *testing.T) {
	doc, err := LoadRoleExclusions()
	testutil.FailErr(t, "load exclusions", err)
	for _, kind := range []string{"openai", "together", "gemini", "ollama", "azure"} {
		for _, id := range []string{"gpt-5-nano", "Qwen/Qwen3.5-9B", "Qwen/Qwen3.8-Flash", "gemini-2.0-flash", "model-1b", "tiny"} {
			model := modelinfo.Entry{ID: id, ContextLength: 8192, Capabilities: modelinfo.ModelCapabilities{Tools: modelinfo.Evidence(modelinfo.CapabilitySupported, "fixture")}}
			if got := ModelRoleEligibility(kind, model, PolicySlotCoordinator, doc); got.State != "eligible" {
				t.Fatalf("%s/%s: %+v", kind, id, got)
			}
		}
	}
}

func TestRoleEligibilityRequiresPositiveEvidence(t *testing.T) {
	for _, tc := range []struct {
		tools       modelinfo.CapabilityState
		role, state string
	}{
		{modelinfo.CapabilitySupported, PolicySlotCoordinator, "eligible"},
		{modelinfo.CapabilityUnknown, PolicySlotCoordinator, "unverified"},
		{modelinfo.CapabilityUnsupported, PolicySlotCoordinator, "incompatible"},
		{modelinfo.CapabilityUnknown, PolicySlotAgentPool, "unverified"},
		{modelinfo.CapabilityUnknown, PolicySlotLite, "eligible"},
	} {
		model := modelinfo.Entry{ID: "model", Capabilities: modelinfo.ModelCapabilities{Tools: modelinfo.Evidence(tc.tools, "fixture")}}
		got := ModelRoleEligibility("ollama", model, tc.role, RoleExclusions{})
		if got.State != tc.state {
			t.Fatalf("%+v: %+v", tc, got)
		}
	}
}

func TestRoleExclusionIsProviderAndRoleScoped(t *testing.T) {
	doc := RoleExclusions{Rules: []RoleExclusion{{ID: "closed-tool-interface", Providers: []string{"together"}, Models: []string{"special-*"}, Roles: []string{PolicySlotCoordinator}, Reason: "No tool interface", Evidence: "Fixture"}}}
	model := modelinfo.Entry{ID: "special-chat", Capabilities: modelinfo.ModelCapabilities{Tools: modelinfo.Evidence(modelinfo.CapabilitySupported, "fixture")}}
	if got := ModelRoleEligibility("together", model, PolicySlotCoordinator, doc); got.State != "incompatible" || got.RuleID != "closed-tool-interface" {
		t.Fatalf("missing rule: %+v", got)
	}
	if got := ModelRoleEligibility("together", model, PolicySlotLite, doc); got.State != "eligible" {
		t.Fatalf("utility overblocked: %+v", got)
	}
	if got := ModelRoleEligibility("ollama", model, PolicySlotCoordinator, doc); got.State != "eligible" {
		t.Fatalf("other host overblocked: %+v", got)
	}
}

func TestLoadRoleExclusionsRejectsInvalidRules(t *testing.T) {
	for _, body := range []string{"coordinator: [", "rules: [{id: incomplete}]", "rules: []\nunknown: true"} {
		t.Run(body, func(t *testing.T) {
			configtest.Overlay(t, map[config.Rel]string{config.ModelRoleExclusions: body})
			if _, err := LoadRoleExclusions(); err == nil {
				t.Fatal("invalid exclusions accepted")
			}
		})
	}
}

func TestModelIDPatterns(t *testing.T) {
	for _, tc := range []struct {
		pattern, id string
		want        bool
	}{
		{"Qwen/*-9B", "qwen/qwen3.5-9b", true}, {"special-*", "ordinary", false}, {"re:qwen-[0-9]+", "qwen-9", true}, {"re:qwen-[0-9]+", "prefix-qwen-9", false},
	} {
		got, err := MatchModelIDPattern(tc.pattern, tc.id)
		testutil.FailErr(t, "match pattern", err)
		if got != tc.want {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
	if _, err := MatchModelIDPattern("**", "x"); err == nil {
		t.Fatal("invalid glob accepted")
	}
}
