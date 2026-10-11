package guard_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestObserveCoordinatorCloseoutGrounding_publishesRealOffenders(t *testing.T) {
	root := t.TempDir()
	sess := &api.Session{ID: "parent-1", WorkspacePath: root}
	grepJSON := `{"matches":[{"path":"internal/retention.go","line":1,"content":"package retention"}]}`
	child := []api.Message{
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "grep", ID: "c1", Args: map[string]any{"path": ".", "pattern": "DELETE"}}},
		},
		{Role: api.MessageRoleTool, Content: grepJSON, ToolResult: &api.ToolResult{Content: grepJSON, Outcome: api.ToolResultOutcomeCompleted}},
	}
	envelope := `<task job_id="j1" child_session_id="child-1" agent_type="` + orchestration.ProfileRepoResearcher + `" state="complete">
<task_result>done</task_result>
</task>`
	history := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{Role: api.MessageRoleTool, Content: envelope, WorkerSummary: &api.WorkerSummaryMeta{
			ChildSessionID: "child-1",
			Status:         api.WorkerSummaryStatusComplete,
		}},
	}
	lookup := func(id string) []api.Message {
		if id == "child-1" {
			return child
		}
		return nil
	}
	ledger := ledgertest.CloseoutReader(root, []guidance.EvidenceLeg{{ChildSessionID: "child-1"}}, lookup)
	report := guidance.CoordinatorCompletionReport{
		Synthesis: "phantom path",
		CitedEvidence: []guidance.CoordinatorCitedEvidence{{
			Path: "phantom.go", Line: 1, Excerpt: "package phantom",
		}},
	}
	gc := oar.NewGuardContext()
	verdict, err := guard.ObserveCoordinatorCloseoutGrounding(
		context.Background(), ledger, sess, history, "implement_synthesis", report, []string{"task"}, evidence.CitationRoots{ProjectDir: root}, gc,
	)
	testutil.FailErr(t, "ObserveCoordinatorCloseoutGrounding", err)
	if !verdict.CitationsRequired && len(verdict.UnobservedHandles) == 0 {
		t.Fatal("expected unobserved handle observation")
	}
	if len(gc.Grounding.UnobservedCitedHandles) == 0 {
		t.Fatal("expected real unobserved handle facts")
	}
	found := false
	for _, h := range gc.Grounding.UnobservedCitedHandles {
		if h == "observed" {
			t.Fatal("must not publish fake placeholder offender")
		}
		if h != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("handles=%v", gc.Grounding.UnobservedCitedHandles)
	}
	if gc.RejectData[guidance.SynthHandleNotInLegsCode] == nil {
		t.Fatal("expected PutRejectData for SYNTH_HANDLE_NOT_IN_LEGS")
	}
}
