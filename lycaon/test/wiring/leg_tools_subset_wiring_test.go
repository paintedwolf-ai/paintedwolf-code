package wiring

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

var exploreAgents = []string{
	orchestration.ProfilePathExplorer,
	orchestration.ProfileRepoResearcher,
	orchestration.ProfileCodeReviewer,
	orchestration.ProfileSecurityReviewer,
}

func TestExploreLegToolsMatchExploreProfile(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	policy := h.Sessions.Manager.Coordinator.Guards.Policy()
	builder := compositeWorkerWithPolicy(h.AgentRegistry, policy)

	for _, agentType := range exploreAgents {
		t.Run(agentType, func(t *testing.T) {
			child, err := h.Sessions.Manager.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
				AgentType: agentType,
				Prompt:    "survey",
			})
			testutil.FailErr(t, "SpawnChild", err)
			prof, err := h.AgentRegistry.Get(agentType)
			testutil.FailErr(t, "agents.Get", err)
			schema := sortedToolNamesFromMeta(policy.ListForPrompt(ctx, child, prof.ToolProfile))
			leg, err := builder.BuildWorkerPromptContext(t.Context(), child.ID, child)
			testutil.FailErr(t, "BuildWorkerPromptContext", err)
			got := append([]string(nil), leg.LegTools...)
			sort.Strings(got)
			if !reflect.DeepEqual(got, schema) {
				t.Fatalf("LegTools %v != schema %v", got, schema)
			}
			for _, forbidden := range []string{"edit", "replace_lines", "code_rewrite", "restore_version", "chmod", "chown", "read", "find", "grep", "git_diff"} {
				if legToolsContains(got, forbidden) {
					t.Fatalf("%s LegTools must not include unavailable tool %q: %v", agentType, forbidden, got)
				}
			}
			for _, want := range []string{"command", "complete_leg"} {
				if !legToolsContains(got, want) {
					t.Fatalf("%s LegTools missing %q: %v", agentType, want, got)
				}
			}
		})
	}
}
