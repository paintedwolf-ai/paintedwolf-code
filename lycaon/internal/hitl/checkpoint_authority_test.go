package hitl_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hitl"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestResolveApprovalOptionRecordsEveryInstalledGrant(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	action := hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "combined"}}
	first := hitl.ApprovalGrant{ID: "grant_first", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: sessionID, Title: "Allow for this chat"}
	second := hitl.ApprovalGrant{ID: "grant_second", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: sessionID, Title: "Allow for this chat"}
	presentation, reasons := approvalPlanPresentation()
	presentation.Action = "Use command capabilities"
	presentation.Impact = "Allow both reviewed capabilities."
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{
		Kind: hitl.ApprovalSubjectActionSet, Title: "Allow command capabilities",
		Targets: []hitl.ApprovalTarget{{Kind: "action", Label: "combined"}},
	}, presentation, reasons, []hitl.ApprovalOption{{
		ID: "combined_chat", Kind: hitl.ApprovalOptionLease, Scope: hitl.ApprovalGrantScopeChat,
		Title: "Allow for this chat", Coverage: "both reviewed capabilities",
		ExpiresWhen: "when this chat ends", ReaskWhen: "either capability changes",
		Rung: hitl.ApprovalRungChat, DecisionAction: hitl.ApprovalOptionApprove,
		Authority: []hitl.ApprovalAuthorityDelta{
			{Kind: hitl.AuthorityGenericGrant, Grant: &first},
			{Kind: hitl.AuthorityGenericGrant, Grant: &second},
		},
	}}, hitl.FaceContext{})
	testutil.FailErr(t, "NewApprovalPlan", err)
	resp, err := mgr.RequestCheckpoint(ctx, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &action, ApprovalPlan: plan,
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	mgr.Authority.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	final, err := mgr.Authority.ResolveApprovalOption(ctx, sessionID, resp.CheckpointID, "combined_chat")
	testutil.FailErr(t, "ResolveApprovalOption", err)
	if final.Result == nil || len(final.Result.GrantIDs) != 2 || final.Result.GrantIDs[0] != first.ID || final.Result.GrantIDs[1] != second.ID {
		t.Fatalf("grant ids = %+v", final.Result)
	}
}

func TestApprovalDecisionStampJoinsLaterToolRow(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, t.Context(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval, ToolCallID: "call-stamp",
		ProposedAction: &hitl.ProposedAction{Tool: "command", Command: "git status", Args: map[string]any{"command": "git status"}},
	})
	testutil.FailErr(t, "request checkpoint", err)
	mgr.Authority.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	final, err := mgr.Authority.ResolveApprovalOption(ctx, sessionID, resp.CheckpointID, "approve_current_action")
	testutil.FailErr(t, "resolve checkpoint", err)

	sessions := sessionstore.NewSQL(sqlDB)
	testutil.FailErr(t, "append tool row", sessions.AppendMessages(ctx, sessionID, api.Message{
		ID: "message-stamp", Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{ToolCallID: "call-stamp", Tool: "command", Content: "ok"},
	}))
	messages, err := sessions.GetMessages(ctx, sessionID)
	testutil.FailErr(t, "get messages", err)
	if len(messages) != 1 || messages[0].ToolResult == nil || messages[0].ToolResult.CheckpointDecision == nil {
		t.Fatalf("checkpoint decision missing: %+v", messages)
	}
	decision := messages[0].ToolResult.CheckpointDecision
	if decision.CheckpointID != final.CheckpointID || decision.Subject != "git status" {
		t.Fatalf("checkpoint decision = %+v", decision)
	}
	var remaining int
	testutil.FailErr(t, "count stamps", sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoint_decision_stamps`).Scan(&remaining))
	if remaining != 0 {
		t.Fatalf("remaining stamps = %d", remaining)
	}
}

func TestResolveCheckpointGrantBlockedWhenAuthzLedgerFails(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "hitl-seal-fail.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	sessionID := "sess-hitl-1"
	insertSession(t, sqlDB, sessionID)
	mgr := hitl.NewCheckpoints(hitl.NewSQLStore(sqlDB), &events.Publisher{Hub: events.NewMemoryHub()},
		authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: authzcontext.FailStore{}}})
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:      sessionID,
		Kind:           api.CheckpointKindToolApproval,
		Type:           hitl.DecisionTypeApprove,
		Title:          "Approve command",
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo hi"}},
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	mgr.Authority.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	_, err = mgr.Authority.ResolveApprovalOption(ctx, sessionID, resp.CheckpointID, "approve_current_action")
	if err == nil || !errors.Is(err, authzledger.ErrSealFailed) {
		t.Fatalf("want ErrSealFailed, got %v", err)
	}
	final, err := mgr.PollCheckpoint(ctx, resp.CheckpointID)
	testutil.FailErr(t, "PollCheckpoint", err)
	if final.Status != hitl.DecisionStatusPending {
		t.Fatalf("approve must not apply when ledger append fails; status = %q", final.Status)
	}
}

func TestRepeatDoesNotChangePlanID(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	base := hitl.CheckpointRequest{
		SessionID:      sessionID,
		Kind:           api.CheckpointKindToolApproval,
		Type:           hitl.DecisionTypeApprove,
		Title:          "Approve command",
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo hi"}},
	}
	respBase, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, base)
	testutil.FailErr(t, "RequestCheckpoint base", err)
	respRepeat, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:      base.SessionID,
		Kind:           base.Kind,
		Type:           base.Type,
		Title:          base.Title,
		ProposedAction: base.ProposedAction,
		Repeat: &hitl.RepeatContext{
			ReasonKey: "authority_misuse:aws-cli/s3-remove-bucket",
			Count:     2,
			Subjects:  []string{"aws s3 rb s3://a"},
		},
	})
	testutil.FailErr(t, "RequestCheckpoint repeat", err)
	pending, err := mgr.ListPending(ctx, sessionID, nil)
	testutil.FailErr(t, "ListPending", err)
	var planBase, planRepeat string
	for _, row := range pending {
		if row.ID == respBase.CheckpointID && row.ToolApproval != nil {
			planBase = row.ToolApproval.Plan.ID
		}
		if row.ID == respRepeat.CheckpointID && row.ToolApproval != nil {
			planRepeat = row.ToolApproval.Plan.ID
		}
	}
	if planBase == "" || planRepeat == "" {
		t.Fatalf("plans missing: base=%q repeat=%q pending=%d", planBase, planRepeat, len(pending))
	}
	if planBase != planRepeat {
		t.Fatalf("plan id changed with repeat: base=%q repeat=%q", planBase, planRepeat)
	}
}

func TestManagerResolveExactReplayReturnsCommittedDecision(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:      sessionID,
		Kind:           api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "command"},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)
	approveCurrentOption(t, ctx, mgr, sessionID, resp.CheckpointID)
	replayed, err := mgr.Authority.ResolveApprovalOption(ctx, sessionID, resp.CheckpointID, "approve_current_action")
	testutil.FailErr(t, "replay approval option", err)
	if replayed.Status != hitl.DecisionStatusApproved || replayed.Result == nil || replayed.Result.OptionID != "approve_current_action" {
		t.Fatalf("replayed = %+v", replayed)
	}
}

func TestManagerRequiresPlanOptionForToolApproval(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "command"},
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	_, err = mgr.ResolveCheckpoint(ctx, sessionID, resp.CheckpointID, api.CheckpointKindToolApproval, &hitl.DecisionResult{Approved: true}, nil)
	if !errors.Is(err, hitl.ErrApprovalOptionRequired) {
		t.Fatalf("err = %v, want ErrApprovalOptionRequired", err)
	}
}
