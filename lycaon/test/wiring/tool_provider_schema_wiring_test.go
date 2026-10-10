package wiring

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestToolProfilesListForPromptHaveProviderArgsSchema walks every bundled tool profile
// on a build session — the same path workers and scouts use when the coordinator dispatches.
func TestToolProfilesListForPromptHaveProviderArgsSchema(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, dir)
	testutil.FailErr(t, "create build session", err)

	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)

	policy := h.Sessions.Manager.Coordinator.Guards.Policy()
	for _, prof := range profiles {
		t.Run(prof.ID, func(t *testing.T) {
			metas := policy.ListForPrompt(ctx, sess, prof.ID)
			if len(metas) == 0 {
				t.Fatalf("profile %q listed no tools", prof.ID)
			}
			if err := tools.ValidatePromptToolMetas("profile="+prof.ID, metas); err != nil {
				testutil.FailErr(t, "validate ListForPrompt tool schemas", err)
			}
		})
	}
}

// TestBundledAgentProfilesHaveProviderArgsSchema ensures each agent's tool_profile
// resolves to a prompt surface with provider-safe schemas (worker child sessions).
func TestBundledAgentProfilesHaveProviderArgsSchema(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, dir)
	testutil.FailErr(t, "create parent session", err)

	agents := h.AgentRegistry
	if agents == nil {
		t.Fatal("agent registry not wired")
	}
	seen := map[string]bool{}
	for _, prof := range agents.List() {
		agentType := prof.ID
		profileID := prof.ToolProfile
		if profileID == "" || seen[profileID] {
			continue
		}
		seen[profileID] = true

		t.Run(profileID, func(t *testing.T) {
			child, err := h.Sessions.Manager.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
				AgentType: agentType,
				Prompt:    "schema contract probe",
			})
			testutil.FailErr(t, "SpawnChild", err)
			metas := h.Sessions.Manager.Coordinator.Guards.Policy().ListForPrompt(ctx, child, profileID)
			if len(metas) == 0 {
				t.Fatalf("agent %q profile %q listed no tools", agentType, profileID)
			}
			if err := tools.ValidatePromptToolMetas("agent="+agentType, metas); err != nil {
				testutil.FailErr(t, "validate worker ListForPrompt tool schemas", err)
			}
		})
	}
	if len(seen) == 0 {
		t.Fatal("expected at least one agent tool profile")
	}
}
