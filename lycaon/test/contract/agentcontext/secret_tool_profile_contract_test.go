package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var managedSecretLifecycleTools = []string{"secret_generate", "secret_list", "secret_revoke"}

const managedSecretSkill = "use-secrets-without-reading-them"

func TestManagedSecretLifecycleFollowsMutationCapability(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	for _, profile := range profiles {
		shouldHave := prompts.ToolProfileMutationCapable(profile)
		if shouldHave && (!profile.ToolAllowed("skills_read") || !profile.ToolSticky("skills_read")) {
			t.Errorf("mutation-capable profile %q cannot read the managed-secret procedure", profile.ID)
		}
		for _, name := range managedSecretLifecycleTools {
			if shouldHave {
				if !profile.ToolAllowed(name) {
					t.Errorf("mutation-capable profile %q cannot reach %q", profile.ID, name)
				}
				if profile.ToolDeferred(name) && !profile.ToolSticky("request_tools") {
					t.Errorf("profile %q cannot discover %q", profile.ID, name)
				}
				continue
			}
			if profile.ToolAllowed(name) || !profile.ToolDenied(name) {
				t.Errorf("non-mutating profile %q does not explicitly deny %q", profile.ID, name)
			}
		}
	}
}

func TestMutationCapableAgentsCanReadManagedSecretProcedure(t *testing.T) {
	t.Parallel()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	byID := make(map[string]sandbox.ToolProfile, len(profiles))
	for _, profile := range profiles {
		byID[profile.ID] = profile
	}

	agents, err := agentdef.LoadEffective()
	contractcheck.FailErr(t, "load agent profiles", err)
	for _, agent := range agents {
		profile, ok := byID[agent.ToolProfile]
		if !ok || !prompts.ToolProfileMutationCapable(profile) {
			continue
		}
		if !agent.Skills.Includes(managedSecretSkill) {
			t.Errorf("mutation-capable agent %q cannot read skill %q", agent.ID, managedSecretSkill)
		}
	}
}
