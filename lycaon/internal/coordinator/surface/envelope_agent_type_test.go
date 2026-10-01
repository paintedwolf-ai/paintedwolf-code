package surface

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestLatestTerminalWorkerAgentType(t *testing.T) {
	history := []api.Message{{
		Role:    api.MessageRoleAssistant,
		Content: `<task job_id="j1" agent_type="repo-researcher" state="complete"><summary>done</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID:  "j1",
			AgentType: "repo-researcher",
			Status:    api.WorkerSummaryStatusComplete,
		},
	}}
	if got := LatestTerminalWorkerAgentType(history); got != "repo-researcher" {
		t.Fatalf("agent_type = %q want repo-researcher", got)
	}
}
