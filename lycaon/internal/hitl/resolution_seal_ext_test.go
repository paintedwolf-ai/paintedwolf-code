package hitl_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func sessionEvents(t *testing.T, sqlDB db.Handle, sessionID string) []authzcontext.Event {
	t.Helper()
	rows, err := authzcontext.NewSQLStore(sqlDB).ListEvents(context.Background(), sessionID)
	testutil.FailErr(t, "list authz events", err)
	return rows
}

func eventsByAction(rows []authzcontext.Event, action authzcontext.EventAction) []authzcontext.Event {
	var out []authzcontext.Event
	for _, row := range rows {
		if row.Action == action {
			out = append(out, row)
		}
	}
	return out
}

func detectionCheckpointRequest(sessionID string) hitl.CheckpointRequest {
	return hitl.CheckpointRequest{
		SessionID:  sessionID,
		Kind:       api.CheckpointKindToolApproval,
		ToolCallID: "call-detection",
		ProposedAction: &hitl.ProposedAction{
			Tool: "command", Args: map[string]any{"command": "aws iam create-user"},
			SessionID: sessionID, ActionID: "call-detection",
		},
		Detection: &hitl.DetectionMatch{
			PackID: "pack-cloud", RuleID: "rule-42", RuleTitle: "IAM principal creation", Level: "high",
		},
		DetectionEndpoints: []authzledger.CapabilityEndpoint{{
			Host: "iam.amazonaws.com", Port: 443, Transport: "tcp", Attempts: 1,
		}},
	}
}

// Approving a detection-cited card seals approval_decision and
// detection_resolved in the same transaction that resolves the checkpoint.
func TestApproveSealsDetectionResolvedWithDecision(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, detectionCheckpointRequest(sessionID))
	testutil.FailErr(t, "RequestCheckpoint", err)
	approveCurrentOption(t, ctx, mgr, sessionID, resp.CheckpointID)

	rows := sessionEvents(t, sqlDB, sessionID)
	decisions := eventsByAction(rows, authzcontext.EventActionApprovalDecision)
	if len(decisions) != 1 || decisions[0].Outcome != authzcontext.EventOutcomeAllowed {
		t.Fatalf("approval_decision rows = %+v", decisions)
	}
	resolved := eventsByAction(rows, authzcontext.EventActionDetectionResolved)
	if len(resolved) != 1 || resolved[0].Outcome != authzcontext.EventOutcomeAllowed {
		t.Fatalf("detection_resolved rows = %+v", resolved)
	}
	if resolved[0].ToolName != "command" {
		t.Fatalf("detection_resolved tool = %q", resolved[0].ToolName)
	}
}

// The detection citation is persisted with the pending checkpoint, so a
// restarted manager — with no executor memory — still seals detection_resolved
// and the expired approval_decision when the fail-safe fires.
func TestDetectionResolvedSurvivesRestart(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	mgr.SetCheckpointExpiry(func() time.Duration { return 0 })
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, detectionCheckpointRequest(sessionID))
	testutil.FailErr(t, "RequestCheckpoint", err)

	restarted := hitl.NewManager(hitl.NewSQLStore(sqlDB), nil, authzcontext.SQLRecorder(sqlDB))
	restarted.SetCheckpointExpiry(func() time.Duration { return time.Nanosecond })
	testutil.FailErr(t, "RestorePending", restarted.RestorePending(ctx))

	deadline := time.After(3 * time.Second)
	for {
		final, err := restarted.PollCheckpoint(ctx, resp.CheckpointID)
		testutil.FailErr(t, "PollCheckpoint", err)
		if final.Status == hitl.DecisionStatusExpired {
			break
		}
		select {
		case <-deadline:
			t.Fatal("restored checkpoint did not expire")
		case <-time.After(10 * time.Millisecond):
		}
	}
	rows := sessionEvents(t, sqlDB, sessionID)
	decisions := eventsByAction(rows, authzcontext.EventActionApprovalDecision)
	if len(decisions) != 1 || decisions[0].Outcome != authzcontext.EventOutcomeDenied || decisions[0].ResolvedBy != authzcontext.ResolvedByExpiry {
		t.Fatalf("approval_decision rows = %+v", decisions)
	}
	resolved := eventsByAction(rows, authzcontext.EventActionDetectionResolved)
	if len(resolved) != 1 || resolved[0].Outcome != authzcontext.EventOutcomeDenied || resolved[0].ResolvedBy != authzcontext.ResolvedByExpiry {
		t.Fatalf("detection_resolved rows = %+v", resolved)
	}
}

// A human denial is sealed inside the resolving transaction: when the ledger
// append fails, the resolution fails and the checkpoint stays pending.
func TestRejectSealedInResolvingTransaction(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:      sessionID,
		Kind:           api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "write"},
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	_, err = mgr.ResolveCheckpoint(ctx, sessionID, resp.CheckpointID, api.CheckpointKindToolApproval,
		&hitl.DecisionResult{Approved: false, Comments: "no"}, nil)
	testutil.FailErr(t, "ResolveCheckpoint", err)
	rows := sessionEvents(t, sqlDB, sessionID)
	decisions := eventsByAction(rows, authzcontext.EventActionApprovalDecision)
	if len(decisions) != 1 || decisions[0].Outcome != authzcontext.EventOutcomeDenied || decisions[0].ResolvedBy != authzcontext.ResolvedByHuman {
		t.Fatalf("approval_decision rows = %+v", decisions)
	}
}

func TestRejectFailsClosedWhenLedgerAppendFails(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "hitl-deny-seal.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	sessionID := "sess-hitl-1"
	insertSession(t, sqlDB, sessionID)
	mgr := hitl.NewManager(hitl.NewSQLStore(sqlDB), &events.Publisher{Hub: events.NewMemoryHub()},
		authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: authzcontext.FailStore{}}})
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:      sessionID,
		Kind:           api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "write"},
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	_, err = mgr.ResolveCheckpoint(ctx, sessionID, resp.CheckpointID, api.CheckpointKindToolApproval,
		&hitl.DecisionResult{Approved: false}, nil)
	if !errors.Is(err, authzledger.ErrSealFailed) {
		t.Fatalf("want ErrSealFailed, got %v", err)
	}
	final, err := mgr.PollCheckpoint(ctx, resp.CheckpointID)
	testutil.FailErr(t, "PollCheckpoint", err)
	if final.Status != hitl.DecisionStatusPending {
		t.Fatalf("denial must not resolve unledgered; status = %q", final.Status)
	}
}

// content_apply resolutions land in the authz event chain with the decision.
func TestContentApplySealsHumanGate(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	before := "old\n"
	resp, err := mgr.RequestCheckpoint(ctx, hitl.CheckpointRequest{
		SessionID: sessionID,
		Kind:      api.CheckpointKindContentApply,
		ContentApply: &hitl.ContentApplyPayload{
			Tool: "write", ToolCallID: "call-apply", Path: "notes.md",
			Before: &before, After: "new\n",
		},
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	_, err = mgr.ResolveCheckpoint(ctx, sessionID, resp.CheckpointID, api.CheckpointKindContentApply, nil,
		&hitl.ContentApplyResolve{Decision: api.ContentApplyReject, Guidance: "keep it"})
	testutil.FailErr(t, "ResolveCheckpoint content_apply", err)

	rows := sessionEvents(t, sqlDB, sessionID)
	resolved := eventsByAction(rows, authzcontext.EventActionContentApplyResolved)
	if len(resolved) != 1 || resolved[0].Outcome != authzcontext.EventOutcomeDenied {
		t.Fatalf("content_apply_resolved rows = %+v", resolved)
	}
	if resolved[0].ToolName != "write" {
		t.Fatalf("content_apply_resolved tool = %q", resolved[0].ToolName)
	}
}
