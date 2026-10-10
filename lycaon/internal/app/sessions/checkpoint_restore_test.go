package sessions

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCheckpointRestartRestoresJoinedApprovalAndDurableDenial(t *testing.T) {
	database := testdbfixture.Open(t, "checkpoint-restore.db")
	testdbseed.InsertSession(t, database, "chat", testdbseed.DefaultProjectID)
	saved := hitl.NewSQLStore(database)
	resolvedAt := time.Now()
	for _, row := range []hitl.StoredCheckpoint{
		{ID: "pending", SessionID: "chat", ProjectID: testdbseed.DefaultProjectID, Kind: api.CheckpointKindToolApproval, Status: hitl.DecisionStatusPending, Type: hitl.DecisionTypeApprove, Title: "Pending command", ToolName: "command", CreatedAt: time.Now(), Payload: map[string]any{"coalesce_chat": "chat", "coalesce_grant_key": "pending-key", "joined_count": 3, "joined_tool_call_ids": []any{"call-1", "call-2", "call-3"}}},
		{ID: "rejected", SessionID: "chat", ProjectID: testdbseed.DefaultProjectID, Kind: api.CheckpointKindToolApproval, Status: hitl.DecisionStatusRejected, ResolvedAt: &resolvedAt, Type: hitl.DecisionTypeApprove, Title: "Rejected command", ToolName: "command", CreatedAt: time.Now(), Payload: map[string]any{"coalesce_grant_key": "denied-key"}},
	} {
		testutil.FailErr(t, "persist approval before restart", saved.Insert(t.Context(), row))
	}
	checkpoints := hitl.NewCheckpoints(saved, nil, authzcontext.SQLRecorder(database))
	checkpoints.SetCheckpointExpiry(func() time.Duration { return 0 })
	coalesce := approvalstate.NewToolApprovalCoalesce()
	testutil.FailErr(t, "restore approval checkpoint hooks", wireToolApprovalCheckpointHooks(t.Context(), checkpoints, coalesce))
	action, id := coalesce.Begin("chat", "pending-key")
	if action != approvalstate.ToolApprovalCoalesceJoin || id != "pending" {
		t.Fatalf("restart duplicated pending approval: %v,%q", action, id)
	}
	action, _ = coalesce.Begin("chat", "denied-key")
	if action != approvalstate.ToolApprovalCoalesceSkipDenied {
		t.Fatalf("restart forgot durable denial: %v", action)
	}
}
