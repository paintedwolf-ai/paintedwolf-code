package hitl_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestManagerPublishCheckpointSSE(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "hitl-sse.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)

	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	sessionID := "sess-hitl-sse"
	insertSession(t, sqlDB, sessionID)

	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), pub, authzcontext.SQLRecorder(sqlDB))

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: testdbseed.DefaultProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID,
		Kind:      api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "write",
},
Scope: hitl.ActionScope{
ProjectDir: projectDir,
},
},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)
	assertCheckpointEvent(t, ch, resp.CheckpointID, api.CheckpointStatusPending)

	approveCurrentOption(t, ctx, mgr, sessionID, resp.CheckpointID)
	assertCheckpointEvent(t, ch, resp.CheckpointID, api.CheckpointStatusApproved)
}

func TestManagerCheckpointRoutesByProjectID(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "hitl-route.db")

	const projectID = "11111111-2222-3333-4444-555555555555"
	sessionID := "sess-route"
	testdbseed.InsertSession(t, sqlDB, sessionID, projectID)

	hub := events.NewMemoryHub()
	mgr := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), &events.Publisher{Hub: hub}, authzcontext.SQLRecorder(sqlDB))

	// Subscribe by canonical project UUID.
	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: projectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	// Route by ProjectID, not ProjectDir.
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID,
		Kind:      api.CheckpointKindToolApproval,
		ProjectID: projectID,
		ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "git push origin main"},
},
Scope: hitl.ActionScope{
ProjectDir: "/on/disk/project/path",
},
},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)
	assertCheckpointEvent(t, ch, resp.CheckpointID, api.CheckpointStatusPending)
}

func TestManagerContentApplyRoutesByProjectID(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "hitl-ca-route.db")

	const projectID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	sessionID := "sess-ca-route"
	testdbseed.InsertSession(t, sqlDB, sessionID, projectID)

	hub := events.NewMemoryHub()
	mgr := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), &events.Publisher{Hub: hub}, authzcontext.SQLRecorder(sqlDB))

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: projectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	resp, err := mgr.RequestCheckpoint(ctx, hitl.CheckpointRequest{
		SessionID: sessionID,
		Kind:      api.CheckpointKindContentApply,
		ProjectID: projectID,
		ContentApply: &hitl.ContentApplyPayload{
			Tool:  "write",
			Path:  "src/a.go",
			After: "package a\n",
		},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)
	assertCheckpointEvent(t, ch, resp.CheckpointID, api.CheckpointStatusPending)
}

func TestPatchPendingAIRationaleNoSSEAfterResolve(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "hitl-rationale.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)

	const projectID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	sessionID := "sess-rationale"
	testdbseed.InsertSession(t, sqlDB, sessionID, projectID)

	hub := events.NewMemoryHub()
	mgr := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), &events.Publisher{Hub: hub}, authzcontext.SQLRecorder(sqlDB))

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: projectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:      sessionID,
		Kind:           api.CheckpointKindToolApproval,
		ProjectID:      projectID,
		ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "git push"},
},
},
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	assertCheckpointEvent(t, ch, resp.CheckpointID, api.CheckpointStatusPending)

	// Successful patch while pending publishes and surfaces ai_rationale.
	if err := mgr.PatchPendingToolApprovalAIRationale(ctx, resp.CheckpointID, "Pushes the fix the user asked for."); err != nil {
		testutil.FailErr(t, "PatchPending while pending", err)
	}
	select {
	case envelope := <-ch:
		var ev api.CheckpointEvent
		if err := json.Unmarshal(envelope.Data, &ev); err != nil {
			testutil.FailErr(t, "unmarshal patched event", err)
		}
		if ev.Status != api.CheckpointStatusPending {
			t.Fatalf("patched status = %q", ev.Status)
		}
		if ev.ToolApproval == nil || ev.ToolApproval.AIRationale != "Pushes the fix the user asked for." {
			t.Fatalf("ai_rationale = %+v", ev.ToolApproval)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for ai_rationale SSE")
	}

	approveCurrentOption(t, ctx, mgr, sessionID, resp.CheckpointID)
	assertCheckpointEvent(t, ch, resp.CheckpointID, api.CheckpointStatusApproved)

	// Stale patch after resolve must no-op the write and publish no SSE.
	if err := mgr.PatchPendingToolApprovalAIRationale(ctx, resp.CheckpointID, "should not appear"); err != nil {
		testutil.FailErr(t, "PatchPending after resolve", err)
	}
	select {
	case envelope := <-ch:
		t.Fatalf("unexpected SSE after resolved patch: %+v", envelope)
	case <-time.After(150 * time.Millisecond):
	}

	row, err := mgr.Store.Get(ctx, resp.CheckpointID)
	testutil.FailErr(t, "store.Get", err)
	if got, _ := row.Payload["ai_rationale"].(string); got == "should not appear" {
		t.Fatal("resolved patch overwrote ai_rationale")
	}
}

func TestClearPendingAIRationale(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "hitl-rationale-clear.db")

	const projectID = "aaaaaaaa-bbbb-cccc-dddd-ffffffffffff"
	sessionID := "sess-rationale-clear"
	testdbseed.InsertSession(t, sqlDB, sessionID, projectID)

	hub := events.NewMemoryHub()
	mgr := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), &events.Publisher{Hub: hub}, authzcontext.SQLRecorder(sqlDB))

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: projectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:          sessionID,
		Kind:               api.CheckpointKindToolApproval,
		ProjectID:          projectID,
		ProposedAction:     &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "git push"},
},
},
		AIRationalePending: true,
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	assertCheckpointEvent(t, ch, resp.CheckpointID, api.CheckpointStatusPending)

	// The reserved-slot flag is surfaced on the initial pending event.
	row, err := mgr.Store.Get(ctx, resp.CheckpointID)
	testutil.FailErr(t, "store.Get", err)
	if pending, _ := row.Payload["ai_rationale_pending"].(bool); !pending {
		t.Fatal("expected ai_rationale_pending set on the pending row")
	}

	// Fail-soft clear while pending republishes with the flag gone.
	if err := mgr.ClearPendingToolApprovalAIRationale(ctx, resp.CheckpointID); err != nil {
		testutil.FailErr(t, "ClearPending while pending", err)
	}
	select {
	case envelope := <-ch:
		var ev api.CheckpointEvent
		if err := json.Unmarshal(envelope.Data, &ev); err != nil {
			testutil.FailErr(t, "unmarshal cleared event", err)
		}
		if ev.ToolApproval == nil || ev.ToolApproval.AIRationalePending {
			t.Fatalf("ai_rationale_pending should be cleared: %+v", ev.ToolApproval)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for cleared SSE")
	}

	// A second clear is a no-op (flag already gone) — no SSE.
	if err := mgr.ClearPendingToolApprovalAIRationale(ctx, resp.CheckpointID); err != nil {
		testutil.FailErr(t, "ClearPending idempotent", err)
	}
	select {
	case envelope := <-ch:
		t.Fatalf("unexpected SSE on idempotent clear: %+v", envelope)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestJoinerBandEscalationReachesWire(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID,
		Kind:      api.CheckpointKindToolApproval,
		Type:      hitl.DecisionTypeApprove,
		ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Presentation: hitl.ActionPresentation{
Command: "git status",
},
},
		ConsequenceBand: api.ConsequenceBandStandard,
	})
	testutil.FailErr(t, "RequestCheckpoint", err)

	err = mgr.PatchPendingToolApprovalJoined(ctx, resp.CheckpointID, 2,
		[]string{"tc-1", "tc-2"}, string(api.ConsequenceBandHighRisk), string(api.ConsequenceCodeDetection))
	testutil.FailErr(t, "PatchPendingToolApprovalJoined", err)

	pending, err := mgr.ListPending(ctx, sessionID, nil)
	testutil.FailErr(t, "ListPending", err)
	if len(pending) != 1 || pending[0].ToolApproval == nil {
		t.Fatalf("pending = %+v", pending)
	}
	payload := pending[0].ToolApproval
	if payload.ConsequenceBand != api.ConsequenceBandHighRisk {
		t.Fatalf("payload band = %q want high_risk", payload.ConsequenceBand)
	}
	if payload.ConsequenceCode != api.ConsequenceCodeDetection {
		t.Fatalf("payload code = %q want detection", payload.ConsequenceCode)
	}
	if payload.Plan.Presentation.ConsequenceBand != api.ConsequenceBandStandard {
		t.Fatalf("plan presentation band = %q want standard (immutable)", payload.Plan.Presentation.ConsequenceBand)
	}
}
