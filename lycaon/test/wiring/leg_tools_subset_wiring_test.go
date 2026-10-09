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

var exploreAgentsCommandFree = []string{
	orchestration.ProfilePathExplorer,
	orchestration.ProfileRepoResearcher,
	orchestration.ProfileCodeReviewer,
	orchestration.ProfileSecurityReviewer,
}

func TestExploreLegToolsSubsetHasNoCommand(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	dir := t.TempDir()
	parent, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session", err)
	policy := h.SessionMgr.Coordinator.Guards.Policy()
	builder := compositeWorkerWithPolicy(h.AgentRegistry, policy)

	for _, agentType := range exploreAgentsCommandFree {
		t.Run(agentType, func(t *testing.T) {
			child, err := h.SessionMgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
				AgentType: agentType,
				Prompt:    "survey",
			})
			testutil.FailErr(t, "SpawnChild", err)
			prof, err := h.AgentRegistry.Get(agentType)
			testutil.FailErr(t, "agents.Get", err)
			schema := sortedToolNamesFromMeta(policy.ListForPrompt(ctx, child, prof.ToolProfile))
			leg, err := builder.BuildWorkerPromptContext(child.ID, child)
			testutil.FailErr(t, "BuildWorkerPromptContext", err)
			got := append([]string(nil), leg.LegTools...)
			sort.Strings(got)
			if !reflect.DeepEqual(got, schema) {
				t.Fatalf("LegTools %v != schema %v", got, schema)
			}
			for _, name := range got {
				if name == "command" {
					t.Fatalf("%s LegTools must not include command: %v", agentType, got)
				}
			}
			for _, want := range []string{"find", "grep", "read"} {
				if !legToolsContains(got, want) {
					t.Fatalf("%s LegTools missing %q: %v", agentType, want, got)
				}
			}
			if agentType == orchestration.ProfileCodeReviewer && !legToolsContains(got, "git_diff") {
				t.Fatalf("%s LegTools missing git_diff: %v", agentType, got)
			}
		})
	}
}
