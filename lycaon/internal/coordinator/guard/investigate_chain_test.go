package guard_test

import (
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func readToolMessages(path, body string) []api.Message {
	return []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{
				Name: "read",
				ID:   "r1",
				Args: map[string]any{"path": path},
			}},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    body,
			ToolResult: &api.ToolResult{Content: body, Outcome: api.ToolResultOutcomeCompleted},
		},
	}
}

func TestLastInvestigateChainBoundary_multiTurnReadSurvivesUserFollowUp(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "inspect auth"},
	}
	history = append(history, readToolMessages("src/foo.go", "line 42: token check\n")...)
	history = append(history, api.Message{Role: api.MessageRoleUser, Content: "explain what you found"})
	if got := guard.LastInvestigateChainBoundary(history); got != 0 {
		t.Fatalf("boundary = %d want 0 (read stays in investigate chain)", got)
	}
	msgs := guard.CoordinatorToolMessagesSinceInvestigateChain(history)
	if len(msgs) != len(history) {
		t.Fatalf("chain msgs len = %d want full slice %d since boundary 0", len(msgs), len(history))
	}
}

func TestLastInvestigateChainBoundary_breaksOnTaskDispatch(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "fix auth"},
	}
	history = append(history, readToolMessages("src/foo.go", "package foo\n")...)
	history = append(history, api.Message{
		Role: api.MessageRoleAssistant,
		ToolCalls: []api.ToolCall{{
			Name: "task",
			ID:   "t1",
			Args: map[string]any{"agent_type": "implementer", "brief": map[string]any{"goal": "fix", "done_when": []any{"done"}}},
		}},
	})
	if got := guard.LastInvestigateChainBoundary(history); got != len(history) {
		t.Fatalf("boundary = %d want %d after task()", got, len(history))
	}
}

func TestLastInvestigateChainBoundary_breaksOnWorkerEnvelope(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "fix auth"},
	}
	history = append(history, readToolMessages("src/foo.go", "package foo\n")...)
	history = append(history, api.Message{
		Role:    api.MessageRoleAssistant,
		Content: `<task job_id="j1" agent_type="implementer" state="complete"><summary>done</summary></task>`,
		WorkerSummary: &api.WorkerSummaryMeta{
			WorkerID: "j1",
			Status:   "complete",
		},
	})
	if got := guard.LastInvestigateChainBoundary(history); got != len(history) {
		t.Fatalf("boundary = %d want %d after worker envelope", got, len(history))
	}
	msgs := guard.CoordinatorToolMessagesSinceInvestigateChain(history)
	if len(msgs) != 0 {
		t.Fatalf("post-orchestrate chain msgs = %d want 0", len(msgs))
	}
}

func TestLastInvestigateChainBoundaryIgnoresUnstructuredTaskXML(t *testing.T) {
	history := []api.Message{{
		Role:    api.MessageRoleAssistant,
		Content: `<task job_id="j1" agent_type="implementer" state="complete"><summary>done</summary></task>`,
	}}
	if got := guard.LastInvestigateChainBoundary(history); got != 0 {
		t.Fatalf("boundary = %d want 0 without structured worker metadata", got)
	}
}
