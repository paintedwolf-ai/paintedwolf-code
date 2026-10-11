package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The coordinator surface machinery keys on the coordinator profile id, so a
// posture must not change the profile a root session resolves to.
func TestPostureNeverSelectsToolProfile(t *testing.T) {
	t.Parallel()
	agents := orchestration.NewMemoryAgentRegistry()
	contractcheck.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(t.Context(), agents))
	for _, posture := range sessionposture.AllSessionPostures() {
		sess := &api.Session{Posture: posture, AgentType: orchestration.ProfileCoordinator}
		if got := profiles.ResolveToolProfile(sess, agents, ""); got != orchestration.ProfileCoordinator {
			t.Fatalf("posture %q profile = %q want %q", posture, got, orchestration.ProfileCoordinator)
		}
	}
}

func TestAgentTypeSelectsToolProfile(t *testing.T) {
	t.Parallel()
	agents := orchestration.NewMemoryAgentRegistry()
	contractcheck.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(t.Context(), agents))
	sess := &api.Session{Posture: api.SessionPostureSpec, AgentType: "implementer"}
	got := profiles.ResolveToolProfile(sess, agents, "")
	if got != "implement" {
		t.Fatalf("agent profile = %q want implement", got)
	}
}

func TestWorkflowCoordinatorProfileOverridesAgent(t *testing.T) {
	t.Parallel()
	agents := orchestration.NewMemoryAgentRegistry()
	contractcheck.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(t.Context(), agents))
	sess := &api.Session{Posture: api.SessionPostureVet, AgentType: orchestration.ProfileCoordinator}
	got := profiles.ResolveToolProfile(sess, agents, "explore_readonly")
	if got != "explore_readonly" {
		t.Fatalf("manifest profile = %q want explore_readonly", got)
	}
}

func TestPostureRegistryRulesMatchBundledPaths(t *testing.T) {
	t.Parallel()
	reg, err := profiles.LoadPostureRegistry()
	contractcheck.FailErr(t, "profiles.LoadPostureRegistry failed", err)
	wantFiles := map[api.SessionPosture]string{
		api.SessionPostureSpec:        "config/packs/painted-wolf/platform/host/posture-rules/spec.yaml",
		api.SessionPostureBuild:       "config/packs/painted-wolf/platform/host/posture-rules/build.yaml",
		api.SessionPostureOrchestrate: "config/packs/painted-wolf/platform/host/posture-rules/orchestrate.yaml",
		api.SessionPostureVet:         "config/packs/painted-wolf/platform/host/posture-rules/vet.yaml",
	}
	for posture, wantPath := range wantFiles {
		spec, err := reg.Get(posture)
		contractcheck.FailErr(t, "reg.Get failed", err)
		if len(spec.Rules) != 1 || spec.Rules[0] != wantPath {
			t.Fatalf("posture %q rules = %v want [%q]", posture, spec.Rules, wantPath)
		}
	}
}
