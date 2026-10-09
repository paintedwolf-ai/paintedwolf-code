package hitl_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func requestExplicitApprovalCheckpoint(t *testing.T, ctx context.Context, mgr *hitl.Checkpoints, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	t.Helper()
	if req.Kind != api.CheckpointKindToolApproval || req.Decision != nil || req.ApprovalPlan != nil || req.SecretScreen != nil {
		t.Fatal("explicit approval fixture requires an uncompiled ordinary tool-approval request")
	}
	verdict, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest,
		ApprovalRequest: &gate.ApprovalRequest{Count: 1},
	}, gate.DefaultPosture)
	if verdict != gate.Ask || decision == nil || decision.Primary != api.GateExplicitApprovalRequest {
		t.Fatalf("explicit approval fixture decision = %s/%+v", verdict, decision)
	}
	req.Decision = decision
	return mgr.RequestCheckpoint(ctx, req)
}

func requestSecretApprovalCheckpoint(t *testing.T, ctx context.Context, mgr *hitl.Checkpoints, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	t.Helper()
	if req.Kind != api.CheckpointKindToolApproval || req.Decision != nil || req.ApprovalPlan != nil || req.SecretScreen == nil {
		t.Fatal("secret approval fixture requires an uncompiled secret-screen request")
	}
	verdict, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSend, Ran: gate.ProducerPayload,
		Payload: &gate.SecretHit{
			Surface: req.SecretScreen.Surface, RuleID: req.SecretScreen.RuleID,
			RuleTitle:   req.SecretScreen.RuleTitle,
			Occurrences: req.SecretScreen.Occurrences, SourceKind: req.SecretScreen.SourceKind,
			SourceTool: req.SecretScreen.SourceTool,
		},
	}, gate.DefaultPosture)
	if verdict != gate.Ask || decision == nil || decision.Primary != api.GateSecretOutbound {
		t.Fatalf("secret approval fixture decision = %s/%+v", verdict, decision)
	}
	req.Decision = decision
	return mgr.RequestCheckpoint(ctx, req)
}

func TestManagerRequestApproveResolve(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID,
		Kind:      api.CheckpointKindToolApproval,
		Type:      hitl.DecisionTypeApprove,
		Title:     "Approve command",
		ProposedAction: &hitl.ProposedAction{
			Tool: "command",
			Args: map[string]any{"command": "echo hi"},
		},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)
	if resp.Status != hitl.DecisionStatusPending {
		t.Fatalf("status = %q", resp.Status)
	}

	pending, err := mgr.ListPending(ctx, sessionID, nil)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v err=%v", pending, err)
	}
	if pending[0].ToolApproval == nil || pending[0].ToolApproval.Plan.Subject.Targets[0].Details["args"].(map[string]any)["command"] != "echo hi" {
		t.Fatalf("pending tool_approval = %+v", pending[0].ToolApproval)
	}

	approveCurrentOption(t, ctx, mgr, sessionID, resp.CheckpointID)
	final, err := mgr.PollCheckpoint(ctx, resp.CheckpointID)
	testutil.FailErr(t, "mgr.PollCheckpoint failed", err)
	if final.Status != hitl.DecisionStatusApproved {
		t.Fatalf("final status = %q", final.Status)
	}
	var operationStatus string
	testutil.FailErr(t, "read approval operation", sqlDB.QueryRowContext(ctx, `SELECT status FROM approval_operations WHERE checkpoint_id = ?`, resp.CheckpointID).Scan(&operationStatus))
	if operationStatus != "committed" {
		t.Fatalf("approval operation status = %q", operationStatus)
	}
}

func TestManagerPersistsRedactedApprovalArguments(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	secret := "cargo-token-that-must-not-be-persisted"
	command := "cargo publish --token " + secret
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{
			Tool: "command", Command: command,
			Args: map[string]any{"command": command, "token": secret},
		},
	})
	testutil.FailErr(t, "request redacted checkpoint", err)
	var argsJSON, payloadJSON string
	testutil.FailErr(t, "read persisted checkpoint", sqlDB.QueryRowContext(ctx,
		`SELECT args_json, payload_json FROM checkpoints WHERE id = ?`, resp.CheckpointID,
	).Scan(&argsJSON, &payloadJSON))
	stored := argsJSON + payloadJSON
	if strings.Contains(stored, secret) || !strings.Contains(stored, "[REDACTED]") {
		t.Fatalf("checkpoint persisted credential: %s", stored)
	}
}

func TestCancelPendingForSessionResolvesCheckpointWithoutAuthority(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID,
		Kind:      api.CheckpointKindToolApproval,
		Type:      hitl.DecisionTypeApprove,
		Title:     "Approve command",
		ProposedAction: &hitl.ProposedAction{
			Tool: "command",
			Args: map[string]any{"command": "echo hi"},
		},
	})
	testutil.FailErr(t, "request checkpoint", err)
	mgr.Sessions.SetSessionAdmission(func(context.Context, string, func() error) error {
		return lifecycle.ErrStopping
	})
	if _, err := mgr.Authority.ResolveApprovalOption(ctx, sessionID, resp.CheckpointID, "approve_current_action"); !errors.Is(err, lifecycle.ErrStopping) {
		t.Fatalf("approval during stop admission = %v, want session stopping", err)
	}
	pendingBeforeStop, err := mgr.PollCheckpoint(ctx, resp.CheckpointID)
	testutil.FailErr(t, "poll still-pending checkpoint", err)
	if pendingBeforeStop.Status != hitl.DecisionStatusPending {
		t.Fatalf("status after rejected approval = %q, want pending", pendingBeforeStop.Status)
	}

	testutil.FailErr(t, "cancel pending checkpoints", mgr.CancelPendingForSession(ctx, sessionID, "user stopped"))
	final, err := mgr.PollCheckpoint(ctx, resp.CheckpointID)
	testutil.FailErr(t, "poll canceled checkpoint", err)
	if final.Status != hitl.DecisionStatusCanceled {
		t.Fatalf("status = %q, want canceled", final.Status)
	}
	if final.Result == nil || final.Result.Approved {
		t.Fatalf("result = %+v, want non-authorizing terminal result", final.Result)
	}
	var status, resolvedBy string
	testutil.FailErr(t, "read canceled checkpoint", sqlDB.QueryRowContext(ctx,
		`SELECT status, resolved_by FROM checkpoints WHERE id = ?`, resp.CheckpointID,
	).Scan(&status, &resolvedBy))
	if status != string(hitl.DecisionStatusCanceled) || resolvedBy != authzledger.ResolvedByUserStop {
		t.Fatalf("persisted status/resolved_by = %q/%q", status, resolvedBy)
	}
	events := sessionEvents(t, sqlDB, sessionID)
	decisions := eventsByAction(events, authzcontext.EventActionApprovalDecision)
	if len(decisions) != 1 || decisions[0].Outcome != authzcontext.EventOutcomeDenied || decisions[0].ResolvedBy != authzcontext.ResolvedByUserStop {
		t.Fatalf("authorization decision = %+v, want denied by user_stop", decisions)
	}
	pending, err := mgr.ListPending(ctx, sessionID, nil)
	testutil.FailErr(t, "list pending", err)
	if len(pending) != 0 {
		t.Fatalf("pending checkpoints = %d, want zero", len(pending))
	}
}

func TestManagerPersistsHostPresentationCommandForToolApproval(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)

	_, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID,
		Kind:      api.CheckpointKindToolApproval,
		Type:      hitl.DecisionTypeApprove,
		ProposedAction: &hitl.ProposedAction{
			Tool:    "command_stop",
			Args:    map[string]any{"handle": "process-123"},
			Command: "npm run dev",
		},
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	pending, err := mgr.ListPending(ctx, sessionID, nil)
	testutil.FailErr(t, "ListPending", err)
	if len(pending) != 1 || pending[0].ToolApproval == nil {
		t.Fatalf("pending = %+v", pending)
	}
	if pending[0].ToolApproval.Plan.Presentation.Command != "npm run dev" {
		t.Fatalf("command = %q want npm run dev", pending[0].ToolApproval.Plan.Presentation.Command)
	}
	args, _ := pending[0].ToolApproval.Plan.Subject.Targets[0].Details["args"].(map[string]any)
	if args["handle"] != "process-123" {
		t.Fatalf("args = %+v", args)
	}
}

func TestManagerRejectsPlanForDifferentAction(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	planned := hitl.ProposedAction{Tool: "command", Command: "first", Args: map[string]any{"command": "first"}}
	presentation, reasons := approvalPlanPresentation()
	plan, err := hitl.NewApprovalPlan(planned, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectAction, Title: "First",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "first"}},
	}, presentation, reasons, []hitl.ApprovalOption{hitl.CurrentActionOption()}, hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	other := hitl.ProposedAction{Tool: "command", Command: "second", Args: map[string]any{"command": "second"}}
	_, err = mgr.RequestCheckpoint(ctx, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &other, ApprovalPlan: plan,
	})
	if err == nil {
		t.Fatal("plan for a different action must be rejected")
	}
}

func TestManagerRejectSetsApprovalDenied(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:      sessionID,
		Kind:           api.CheckpointKindToolApproval,
		ToolCallID:     "call-denied-guidance",
		ProposedAction: &hitl.ProposedAction{Tool: "write"},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)
	final, err := mgr.ResolveCheckpoint(ctx, sessionID, resp.CheckpointID, api.CheckpointKindToolApproval, &hitl.DecisionResult{
		Approved: false,
		Comments: "Keep the existing file and report what differs.",
	}, nil)
	if err != nil {
		testutil.FailErr(t, "mgr.ResolveCheckpoint failed", err)
	}
	ok, err := mgr.SessionApprovalDenied(ctx, sessionID)
	if err != nil || !ok {
		t.Fatalf("approval_denied = %v err=%v", ok, err)
	}
	if final.Result == nil || final.Result.Comments != "Keep the existing file and report what differs." {
		t.Fatalf("persisted guidance = %+v", final.Result)
	}
}

type noopApprovalInstaller struct{}

func (noopApprovalInstaller) InstallApprovalOption(context.Context, string, hitl.ApprovalOption) (func(), error) {
	return func() {}, nil
}

func approveCurrentOption(t *testing.T, ctx context.Context, mgr *hitl.Checkpoints, sessionID, checkpointID string) {
	t.Helper()
	mgr.Authority.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	_, err := mgr.Authority.ResolveApprovalOption(ctx, sessionID, checkpointID, "approve_current_action")
	testutil.FailErr(t, "ResolveApprovalOption", err)
}

func TestManagerResolveUnknown404(t *testing.T) {
	ctx := context.Background()
	_, mgr, sessionID := newTestManager(t)
	_, err := mgr.ResolveCheckpoint(ctx, sessionID, "missing-id", api.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: true}, nil)
	if !errors.Is(err, hitl.ErrCheckpointNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestManagerResolveWrongSession(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	insertSession(t, sqlDB, "other-session")

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:      sessionID,
		Kind:           api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "write"},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)
	_, err = mgr.ResolveCheckpoint(ctx, "other-session", resp.CheckpointID, api.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: true}, nil)
	if !errors.Is(err, hitl.ErrCheckpointNotFound) {
		t.Fatalf("err = %v want ErrCheckpointNotFound", err)
	}
}

// TestManagerCheckpointRoutesByProjectID uses the canonical project topic.

// TestManagerContentApplyRoutesByProjectID uses the canonical project topic.

func assertCheckpointEvent(t *testing.T, ch <-chan api.EventEnvelope, checkpointID string, status api.CheckpointStatus) {
	t.Helper()
	select {
	case envelope := <-ch:
		if envelope.Topic != api.EventTopicCheckpoint {
			t.Fatalf("topic = %q want checkpoint", envelope.Topic)
		}
		var ev api.CheckpointEvent
		if err := json.Unmarshal(envelope.Data, &ev); err != nil {
			testutil.FailErr(t, "unmarshal JSON document", err)
		}
		if ev.ID != checkpointID || ev.Status != status {
			t.Fatalf("event = %+v want checkpoint=%s status=%s", ev, checkpointID, status)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for checkpoint SSE event")
	}
}

func newTestManager(t *testing.T) (db.ReadHandle, *hitl.Checkpoints, string) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "hitl.db")
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), pub, authzcontext.SQLRecorder(sqlDB))
	t.Cleanup(mgr.StopExpiryTimers)
	return sqlDB, mgr, "sess-hitl-1"
}

func insertSession(t *testing.T, sqlDB db.Handle, sessionID string) {
	t.Helper()
	testdbseed.InsertSession(t, sqlDB, sessionID, testdbseed.DefaultProjectID)
}

// TestCheckpointAutoExpiresToDenied verifies pending checkpoint expiry.

// insertUserIntentMessage seeds one visible user message, the row shape that
// idx_messages_user_turn and the intent boundary agree on.
func insertUserIntentMessage(t *testing.T, sqlDB db.Handle, sessionID, messageID string, at time.Time) {
	t.Helper()
	entryID := messageID + "-entry"
	testdbseed.InsertSessionEntry(t, sqlDB, entryID, sessionID, "utterance", messageID, 1)
	_, err := sqlDB.ExecContext(context.Background(), `
INSERT INTO messages(id, entry_id, session_id, role, content, origin, authority, trust_tier, seq, ord, ts)
VALUES(?, ?, ?, 'user', 'carry on', 'user', 'user', 'trusted', 1, 1, ?)`,
		messageID, entryID, sessionID, db.FormatTime(at))
	testutil.FailErr(t, "insert user intent message", err)
}
