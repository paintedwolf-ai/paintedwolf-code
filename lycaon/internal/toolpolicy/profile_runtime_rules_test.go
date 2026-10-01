package toolpolicy

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestProfileRuntimeRules_NoRulesAllowsRepoResearcherTask(t *testing.T) {
	rules := &ProfileRuntimeRules{byProfile: map[string][]ProfileRuntimeRule{}}
	sess := &api.Session{ID: "s1", AgentType: "coordinator"}
	err := rules.EvaluateCoordinator(sess, "task", map[string]any{"agent_type": "repo-researcher"}, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
}
