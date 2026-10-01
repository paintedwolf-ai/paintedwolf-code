package orchestration

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func loadAgentRegistryFromConfig(t *testing.T) *MemoryAgentRegistry {
	t.Helper()
	reg := NewMemoryAgentRegistry()
	if err := LoadRequiredAgentRegistry(context.Background(), reg); err != nil {
		testutil.FailErr(t, "LoadRequiredAgentRegistry failed", err)
	}
	if err := ValidateGateAgents(reg); err != nil {
		testutil.FailErr(t, "ValidateGateAgents failed", err)
	}
	return reg
}

func loadBoundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	cfg, err := sandbox.LoadConfig()
	testutil.FailErr(t, "load sandbox config", err)
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	return sandbox.NewBoundary(cfg, profiles)
}

func TestResolveForGateFunctionalAgents(t *testing.T) {
	reg := loadAgentRegistryFromConfig(t)
	cases := map[evidence.GateType]string{
		evidence.GateTypeVerify:           "implementer",
		evidence.GateTypeTest:             "implementer",
		evidence.GateTypeSecurity:         "security-reviewer",
		evidence.GateTypeReview:           "code-reviewer",
		evidence.GateTypePlanReview:       "plan-reviewer",
		evidence.GateTypePlanReviewAlt:    "plan-reviewer-alt",
		evidence.GateTypeSurveyClaims:     "security-reviewer",
		evidence.GateTypeSurveyChallenged: "skeptic",
		evidence.GateTypeOptionsJudge:     "skeptic",
	}
	for gate, wantID := range cases {
		p, err := reg.ResolveForGate(gate)
		if err != nil {
			t.Fatalf("ResolveForGate(%q): %v", gate, err)
		}
		if p.ID != wantID {
			t.Fatalf("gate %q agent = %q want %q", gate, p.ID, wantID)
		}
	}
}

func TestTier1AgentsResolveToolProfiles(t *testing.T) {
	reg := loadAgentRegistryFromConfig(t)
	want := map[string]string{
		"coordinator":       "coordinator",
		"implementer":       "implement",
		"repo-researcher":   "explore_readonly",
		"path-explorer":     "explore_readonly",
		"plan-writer":       "plan_write_only",
		"code-reviewer":     "explore_readonly",
		"plan-reviewer":     "plan_review_readonly",
		"plan-reviewer-alt": "plan_review_readonly",
	}
	for agentID, profileID := range want {
		p, err := reg.Get(agentID)
		if err != nil {
			t.Fatalf("Get(%q): %v", agentID, err)
		}
		if p.ToolProfile != profileID {
			t.Fatalf("agent %q tool_profile = %q want %q", agentID, p.ToolProfile, profileID)
		}
	}
}

func TestMisconfiguredSurfaceBlocked(t *testing.T) {
	b := loadBoundary(t)
	policy := tools.NewProfilePolicyEngine(b)
	ctx := context.Background()

	block := func(profileID, tool string) bool {
		decision, err := policy.Evaluate(ctx, platform.PolicyContext{
			ProfileID: profileID,
			ToolName:  tool,
		})
		if err != nil {
			t.Fatalf("Evaluate(%q, %q): %v", profileID, tool, err)
		}
		return decision != nil && decision.Blocked
	}

	if !block("implement", "delegate_dispatch") {
		t.Fatal("implement profile should block delegate_dispatch")
	}
	// Profile grant; the turn surface gates invoke.
	if block("coordinator", "command") {
		t.Fatal("coordinator profile should allow command")
	}
	if block("implement", "write") {
		t.Fatal("implement should allow write")
	}
	if block("implement", "command") {
		t.Fatal("implement should allow command")
	}
	if block("implement", "web_search") {
		t.Fatal("implement should allow web_search")
	}
	if block("implement", "fetch_url") {
		t.Fatal("implement should allow fetch_url")
	}
}

func TestDefaultPipelineUsesFunctionalAgents(t *testing.T) {
	spec, err := LoadTopologyFromFile(extpacks.Bundled(config.PlatformFlows.Join("_topologies", "default-pipeline.yaml")))
	testutil.FailErr(t, "LoadTopologyFromFile failed", err)
	if spec.Pipeline == nil {
		t.Fatal("missing pipeline")
	}
	want := map[string]string{
		"research": "repo-researcher",
		"review":   "code-reviewer",
	}
	for _, stage := range spec.Pipeline.Stages {
		if expected, ok := want[stage.Name]; ok && stage.AgentProfile != expected {
			t.Fatalf("stage %q profile = %q want %q", stage.Name, stage.AgentProfile, expected)
		}
	}
}
