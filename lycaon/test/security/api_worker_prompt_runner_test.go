package security

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestWorkerLegPromptAppendsSummary(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: "implement feature",
			ToolCalls: []llm.MockToolCall{{
				ID:   "w1",
				Name: "write",
				Args: map[string]any{
					"path":    "feature.go",
					"content": "package feature\n",
				},
			}},
			FollowUpText: "implemented feature X",
		},
		{
			Pattern: ".",
			Text:    "acknowledged",
		},
	}}))
	h := wiring.BuildForTest(t, wiring.WithLLMClient(rec))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)
	mgr := h.SessionMgr
	ctx := context.Background()
	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, h.DB, testdbseed.DefaultProjectID, projectDir)

	r, err := h.DelegationMgr.Create(ctx, api.CreateDelegationRequest{
		ProjectID: testdbseed.DefaultProjectID,
		Task:      "implement feature",
		Strategy:  api.HuntStrategyFileBased,
	})
	testutil.FailErr(t, "h.DelegationMgr.Create failed", err)
	dep, err := h.DelegationMgr.GetStatus(ctx, r.ID)
	testutil.FailErr(t, "h.DelegationMgr.GetStatus failed", err)
	parentID := dep.CoordinatorSessionID
	legID := r.Legs[0].ID
	dispatched, err := h.DelegationMgr.DispatchLeg(ctx, r.ID, legID, "")
	if err != nil {
		testutil.FailErr(t, "h.DelegationMgr.DispatchLeg failed", err)
	}

	var approvalErr error
	var approvalsResolved int
	var childSessionID string
	resolver, ok := h.CheckpointMgr.(*hitl.Checkpoints)
	if !ok {
		t.Fatalf("checkpoint manager = %T, want approval option resolver", h.CheckpointMgr)
	}
	if !testutil.WaitForNoFatal(15*time.Second, func() bool {
		if task, ok := h.WorkerQueue.Get(dispatched.WorkerID); ok {
			childSessionID = task.ChildSessionID
		}
		if childSessionID == "" {
			return false
		}
		kind := api.CheckpointKindToolApproval
		pending, err := h.CheckpointMgr.ListPending(ctx, childSessionID, &kind)
		if err != nil {
			approvalErr = err
			return false
		}
		for _, checkpoint := range pending {
			_, approvalErr = resolver.Authority.ResolveApprovalOption(
				ctx, childSessionID, checkpoint.ID, "approve_current_action",
			)
			if approvalErr != nil {
				return false
			}
			approvalsResolved++
		}
		msgs, err := mgr.Transcript.GetMessages(ctx, parentID)
		if err != nil {
			return false
		}
		for _, msg := range msgs {
			if msg.Role != api.MessageRoleTool || msg.WorkerSummary == nil || msg.WorkerSummary.LegID != legID {
				continue
			}
			if strings.Contains(msg.WorkerSummary.Envelope, `<task`) {
				return true
			}
		}
		return false
	}) {
		parentMessages, _ := mgr.Transcript.GetMessages(ctx, parentID)
		t.Fatalf("worker card timed out: child_session_id=%q approvals_resolved=%d approval_err=%v parent_messages=%+v model_requests=%d",
			childSessionID, approvalsResolved, approvalErr, parentMessages, len(rec.AllRequests()))
	}
	msgs, err := mgr.Transcript.GetMessages(ctx, parentID)
	testutil.FailErr(t, "mgr.GetMessages failed", err)
	var foundWorkerCard bool
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool || msg.WorkerSummary == nil || msg.WorkerSummary.LegID != legID {
			continue
		}
		foundWorkerCard = true
		if !strings.Contains(msg.WorkerSummary.Envelope, `<task`) {
			t.Fatalf("worker card for leg %s missing completion envelope: %q", legID, msg.WorkerSummary.Envelope)
		}
	}
	if !foundWorkerCard {
		t.Fatalf("parent messages missing worker card for leg %s: msgs=%+v", legID, msgs)
	}
}
