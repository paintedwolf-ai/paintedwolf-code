package store

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

// Stale enrichment preserves a settled checkpoint decision.
func TestUpdateMessageKeepsSettledCheckpointDecision(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "checkpoint-carry.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	s := NewSQL(sqlDB)
	sess, err := s.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	settled := api.Message{
		Role:    api.MessageRoleTool,
		Content: "docker build ok",
		ToolResult: &api.ToolResult{
			Content:    "docker build ok",
			Tool:       "command",
			ToolCallID: "call_862bc9f7",
			CheckpointDecision: &api.CheckpointDecisionMeta{
				CheckpointID: "a45ec7d9-c49d-49a6-9096-01805fd09d5c",
				Kind:         "tool_approval",
				Status:       "approved",
				Tool:         "command",
			},
		},
	}
	testutil.FailErr(t, "append settled tool row", s.AppendMessages(ctx, sess.ID, settled))

	stored, err := s.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read settled row", err)
	if len(stored) != 1 || stored[0].ToolResult.CheckpointDecision == nil {
		t.Fatalf("decision missing after append: %+v", stored)
	}

	// Enrichment omits the newer decision.
	enriched := stored[0]
	enriched.ToolResult = &api.ToolResult{
		Content:    stored[0].ToolResult.Content,
		Tool:       stored[0].ToolResult.Tool,
		ToolCallID: stored[0].ToolResult.ToolCallID,
	}
	enriched.EvidenceHandles = []string{"ev-1"}
	_, err = s.UpdateMessage(ctx, sess.ID, enriched.ID, enriched)
	testutil.FailErr(t, "enrich tool row", err)

	after, err := s.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "read enriched row", err)
	if len(after) != 1 || after[0].ToolResult == nil {
		t.Fatalf("row lost after update: %+v", after)
	}
	if after[0].ToolResult.CheckpointDecision == nil {
		t.Fatal("enrichment withdrew the settled checkpoint decision")
	}
	if got := after[0].ToolResult.CheckpointDecision.CheckpointID; got != "a45ec7d9-c49d-49a6-9096-01805fd09d5c" {
		t.Fatalf("checkpoint id = %q", got)
	}
	if len(after[0].EvidenceHandles) != 1 {
		t.Fatalf("evidence handle lost: %+v", after[0].EvidenceHandles)
	}
}
