package delegation

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDispatchLegStampsAgentType(t *testing.T) {
	_, _, queue, delegationMgr, reg := newDelegationTestManager(t)
	ctx := context.Background()
	dir := t.TempDir()
	projectID := seedDelegationProject(t, reg, dir)
	r, err := delegationMgr.Create(ctx, api.CreateDelegationRequest{
		ProjectID: projectID,
		Task:      "research",
	})
	testutil.FailErr(t, "delegationMgr.Create failed", err)
	legID := r.Legs[0].ID
	leg, err := delegationMgr.Store.GetLeg(ctx, r.ID, legID)
	testutil.FailErr(t, "delegationMgr.Store.GetLeg failed", err)
	leg.AgentType = orchestration.ProfileRepoResearcher
	if err := delegationMgr.Store.UpdateLeg(ctx, *leg); err != nil {
		testutil.FailErr(t, "delegationMgr.Store.UpdateLeg failed", err)
	}
	if _, err := delegationMgr.DispatchLeg(ctx, r.ID, legID, ""); err != nil {
		testutil.FailErr(t, "delegationMgr.DispatchLeg failed", err)
	}
	jobID := ""
	if updated, err := delegationMgr.Store.GetLeg(ctx, r.ID, legID); err == nil {
		jobID = updated.WorkerID
	}
	if jobID == "" {
		t.Fatal("expected worker job id")
	}
	task, ok := queue.Get(jobID)
	if !ok || task == nil {
		t.Fatal("expected worker task")
	}
	if task.AgentType != orchestration.ProfileRepoResearcher {
		t.Fatalf("AgentType = %q want %q", task.AgentType, orchestration.ProfileRepoResearcher)
	}
}

func TestDispatchLegMintsParentTaskCard(t *testing.T) {
	mgr, delegationStore, _, delegationMgr, reg := newDelegationTestManager(t)
	ctx := context.Background()
	dir := t.TempDir()
	projectID := seedDelegationProject(t, reg, dir)
	r, err := delegationMgr.Create(ctx, api.CreateDelegationRequest{
		ProjectID: projectID,
		Task:      "research the options",
	})
	testutil.FailErr(t, "Create", err)
	legID := r.Legs[0].ID
	leg, err := delegationMgr.Store.GetLeg(ctx, r.ID, legID)
	testutil.FailErr(t, "GetLeg", err)
	leg.AgentType = orchestration.ProfilePathExplorer
	if err := delegationMgr.Store.UpdateLeg(ctx, *leg); err != nil {
		testutil.FailErr(t, "UpdateLeg", err)
	}
	if _, err := delegationMgr.DispatchLeg(ctx, r.ID, legID, ""); err != nil {
		testutil.FailErr(t, "DispatchLeg", err)
	}
	sessionID, ok := delegationStore.SessionID(r.ID)
	if !ok {
		t.Fatal("expected delegation session")
	}
	msgs, err := mgr.Transcript.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "GetMessages", err)
	var tool *api.Message
	for i := range msgs {
		if msgs[i].Role == api.MessageRoleTool && msgs[i].ToolResult != nil && msgs[i].ToolResult.Tool == "task" {
			tool = &msgs[i]
			break
		}
	}
	if tool == nil {
		t.Fatalf("parent transcript missing task card: %+v", msgs)
	}
	if got, _ := tool.ToolResult.ToolArgs["agent_type"].(string); got != orchestration.ProfilePathExplorer {
		t.Fatalf("agent_type = %q want %q", got, orchestration.ProfilePathExplorer)
	}
}

func TestDispatchLegCoordinatorToolCallRecordsOneCard(t *testing.T) {
	mgr, delegationStore, _, delegationMgr, reg := newDelegationTestManager(t)
	ctx := context.Background()
	dir := t.TempDir()
	projectID := seedDelegationProject(t, reg, dir)
	r, err := delegationMgr.Create(ctx, api.CreateDelegationRequest{
		ProjectID: projectID,
		Task:      "research",
	})
	testutil.FailErr(t, "Create", err)
	if _, err := delegationMgr.DispatchLeg(ctx, r.ID, r.Legs[0].ID, "tc-delegate"); err != nil {
		testutil.FailErr(t, "DispatchLeg", err)
	}
	sessionID, ok := delegationStore.SessionID(r.ID)
	if !ok {
		t.Fatal("expected delegation session")
	}
	msgs, err := mgr.Transcript.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "GetMessages", err)
	cards := 0
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleTool && msg.ToolResult != nil && msg.ToolResult.Tool == "task" {
			cards++
			if msg.ToolResult.ToolCallID != "tc-delegate" {
				t.Fatalf("tool_call_id = %q want tc-delegate", msg.ToolResult.ToolCallID)
			}
		}
	}
	if cards != 1 {
		t.Fatalf("task cards = %d want 1", cards)
	}
}
