package workflowadmin

import (
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// The colophon's workforce is what actually ran: the session's model, and the
// worker legs, counted by agent type.
func TestWorkforceFor_CountsLegsByAgent(t *testing.T) {
	sess := &wire.Session{ProviderID: "anthropic", Model: "claude-opus-5"}
	msgs := []wire.Message{
		{WorkflowRunID: "run_1", WorkerSummary: &wire.WorkerSummaryMeta{AgentType: "security-reviewer"}},
		{WorkflowRunID: "run_1", WorkerSummary: &wire.WorkerSummaryMeta{AgentType: "security-reviewer"}},
		{WorkflowRunID: "run_1", WorkerSummary: &wire.WorkerSummaryMeta{AgentType: "skeptic"}},
		{WorkflowRunID: "other", WorkerSummary: &wire.WorkerSummaryMeta{AgentType: "stranger"}},
		{WorkflowRunID: "run_1"},
	}
	inRun := func(m wire.Message) bool { return m.WorkflowRunID == "run_1" }

	got := workforceFor(sess, msgs, inRun)
	if got == nil || got.Model != "claude-opus-5" || got.Provider != "anthropic" {
		t.Fatalf("workforce = %+v, want the session's model", got)
	}
	if len(got.Agents) != 2 || got.Agents[0].Type != "security-reviewer" || got.Agents[0].Legs != 2 || got.Agents[1].Legs != 1 {
		t.Fatalf("agents = %+v, want legs counted per type, other runs excluded", got.Agents)
	}
	if workforceFor(&wire.Session{}, nil, nil) != nil {
		t.Fatal("want nil when nothing is known about what produced the work")
	}
}
