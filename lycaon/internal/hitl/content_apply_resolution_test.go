package hitl_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestManagerContentApplyCommitsHostComposedBytes(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	before := "one\nkeep a\nkeep b\nthree\n"
	resp, err := mgr.RequestCheckpoint(ctx, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindContentApply,
		ContentApply: &hitl.ContentApplyPayload{Tool: "edit", ToolCallID: "call-1", Path: "notes.txt", Before: &before, After: "ONE\nkeep a\nkeep b\nTHREE\n"},
	})
	testutil.FailErr(t, "request content_apply", err)
	pending, err := mgr.ListPending(ctx, sessionID, nil)
	testutil.FailErr(t, "list content_apply", err)
	if len(pending) != 1 || pending[0].ContentApply == nil || len(pending[0].ContentApply.Hunks) != 2 {
		t.Fatalf("pending content_apply = %+v", pending)
	}
	selected := pending[0].ContentApply.Hunks[1].ID
	resolved, err := mgr.ResolveCheckpoint(ctx, sessionID, resp.CheckpointID, api.CheckpointKindContentApply, nil, &hitl.ContentApplyResolve{
		Decision: api.ContentApplyApprovePartial, ApprovedHunks: []string{selected},
	})
	testutil.FailErr(t, "resolve partial content_apply", err)
	if resolved.ContentResult == nil || resolved.ContentResult.FinalAfter != "one\nkeep a\nkeep b\nTHREE\n" {
		t.Fatalf("resolved content = %+v", resolved.ContentResult)
	}
	loaded, err := mgr.PollCheckpoint(ctx, resp.CheckpointID)
	testutil.FailErr(t, "reload content_apply", err)
	if loaded.ContentResult == nil || loaded.ContentResult.FinalAfter != resolved.ContentResult.FinalAfter {
		t.Fatalf("persisted content = %+v", loaded.ContentResult)
	}
}

func TestManagerContentApplyUnknownHunkStaysPending(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	resp, err := mgr.RequestCheckpoint(ctx, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindContentApply,
		ContentApply: &hitl.ContentApplyPayload{Tool: "write", Path: "notes.txt", After: "after"},
	})
	testutil.FailErr(t, "request content_apply", err)
	_, err = mgr.ResolveCheckpoint(ctx, sessionID, resp.CheckpointID, api.CheckpointKindContentApply, nil, &hitl.ContentApplyResolve{
		Decision: api.ContentApplyApprovePartial, ApprovedHunks: []string{"client-invented"},
	})
	if !errors.Is(err, hitl.ErrContentApplyHunkNotFound) {
		t.Fatalf("resolve err = %v", err)
	}
	loaded, err := mgr.PollCheckpoint(ctx, resp.CheckpointID)
	testutil.FailErr(t, "reload content_apply", err)
	if loaded.Status != hitl.DecisionStatusPending {
		t.Fatalf("status = %q, want pending", loaded.Status)
	}
}
