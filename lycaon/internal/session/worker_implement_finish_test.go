package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestObserveImplementerFinishWithoutWrite(t *testing.T) {
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint registry", err)
	child := &api.Session{
		ID:              "child-1",
		ParentSessionID: "parent-1",
		AgentType:       orchestration.ProfileImplementer,
		WorkspacePath:   t.TempDir(),
	}
	rejectFmt := guidance.NewStaticRejectFormatter(hints)
	// A rejected edit provides no artifact proof.
	msgs := []api.Message{
		{
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "tc1", Name: "edit", Args: map[string]any{"path": "game.py"}},
			},
		},
		{
			Role:       api.MessageRoleTool,
			Content:    "Rejected: missing path",
			ToolResult: &api.ToolResult{ToolCallID: "tc1", Outcome: api.ToolResultOutcomeRejected},
		},
	}
	gc := oar.NewGuardContext()
	workercompletion.ObserveImplementerFinishWithoutWrite(
		context.Background(),
		child,
		msgs,
		"I attempted to edit game.py.",
		child.WorkspacePath,
		nil,
		gc,
	)
	if !evaluateHasCode(t, gc, "WORKER_IMPLEMENT_NO_ARTIFACT", oar.AnchorWorkerReportCheck) {
		t.Fatal("expected WORKER_IMPLEMENT_NO_ARTIFACT decision")
	}
	formatted, err := rejectFmt.Format("WORKER_IMPLEMENT_NO_ARTIFACT", gc.RejectData["WORKER_IMPLEMENT_NO_ARTIFACT"])
	testutil.FailErr(t, "Format", err)
	if !strings.Contains(formatted, "WORKER_IMPLEMENT_NO_ARTIFACT") {
		t.Fatalf("reject=%q", formatted)
	}

	gc2 := oar.NewGuardContext()
	workercompletion.ObserveImplementerFinishWithoutWrite(
		context.Background(),
		child,
		[]api.Message{
			{
				Role: api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{
					{ID: "tc1", Name: "write", Args: map[string]any{"path": "game.py"}},
				},
			},
			{Role: api.MessageRoleTool, Content: "Wrote game.py", ToolResult: &api.ToolResult{ToolCallID: "tc1", Outcome: api.ToolResultOutcomeCompleted}},
		},
		"Created game.py",
		child.WorkspacePath,
		nil,
		gc2,
	)
	if evaluateHasCode(t, gc2, "WORKER_IMPLEMENT_NO_ARTIFACT", oar.AnchorWorkerReportCheck) {
		t.Fatal("write proof should not reject")
	}
}
