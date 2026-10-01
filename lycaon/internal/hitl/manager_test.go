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
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func requestExplicitApprovalCheckpoint(t *testing.T, ctx context.Context, mgr *hitl.Manager, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
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

func requestSecretApprovalCheckpoint(t *testing.T, ctx context.Context, mgr *hitl.Manager, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
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
	mgr.SetSessionAdmission(func(context.Context, string, func() error) error {
		return lifecycle.ErrStopping
	})
	if _, err := mgr.ResolveApprovalOption(ctx, sessionID, resp.CheckpointID, "approve_current_action"); !errors.Is(err, lifecycle.ErrStopping) {
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
	mgr.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	final, err := mgr.ResolveApprovalOption(ctx, sessionID, resp.CheckpointID, "combined_chat")
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
	mgr.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	final, err := mgr.ResolveApprovalOption(ctx, sessionID, resp.CheckpointID, "approve_current_action")
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

func TestManagerSecretRedactionRequiresHostCapability(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)

	_, err := requestSecretApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "fetch_url"},
		SecretScreen: &hitl.SecretScreen{
			Surface: "fetch_url", SurfaceLabel: "page fetch",
			RedactionNote: "Redaction is not offered here: no reviewed rewrite exists.",
			DestinationID: "example.com",
			RuleID:        "gitleaks:github-pat", RuleTitle: "GitHub Personal Access Token",
			GenericShape: "abc-a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3 (40 characters)",
			Occurrences:  1, SourceKind: "tool_argument", OriginKind: "field",
		},
	})
	testutil.FailErr(t, "request non-redactable secret approval", err)
	pending, err := mgr.ListPending(ctx, sessionID, nil)
	testutil.FailErr(t, "list non-redactable approval", err)
	if len(pending) != 1 {
		t.Fatalf("pending = %d", len(pending))
	}
	options := pending[0].ToolApproval.Plan.Options
	if pending[0].ToolApproval.Plan.RecommendedOptionID != "send_unchanged" {
		t.Fatalf("face = %q, want send_unchanged", pending[0].ToolApproval.Plan.RecommendedOptionID)
	}
	var redactedDisabled, unchangedPresent bool
	for _, option := range options {
		switch option.ID {
		case "send_redacted":
			redactedDisabled = option.Disabled
			// Disabled coverage states unavailability, not a replacement.
			if option.Coverage != hitl.CoverageRedactionUnavailable {
				t.Fatalf("disabled redaction coverage = %q", option.Coverage)
			}
		case "send_unchanged":
			unchangedPresent = true
		}
	}
	if !redactedDisabled || !unchangedPresent {
		t.Fatalf("non-redactable options = %+v", options)
	}
	// The unavailable choice stays in the face slot; the note says why.
	if note := pending[0].ToolApproval.Plan.Presentation.OptionNote; note == "" {
		t.Fatal("a card with a disabled redacted send must say why")
	}
	mgr.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	_, err = mgr.ResolveApprovalOption(ctx, sessionID, pending[0].ID, "send_redacted")
	if !errors.Is(err, hitl.ErrApprovalOptionUnavailable) {
		t.Fatalf("disabled redaction err = %v, want unavailable", err)
	}
	if targets := pending[0].ToolApproval.Plan.Subject.Targets; len(targets) != 1 ||
		targets[0].Label != "GitHub Personal Access Token" {
		t.Fatalf("credential kind was not surfaced as the approval subject: %+v", targets)
	}
	if shape := pending[0].ToolApproval.Plan.Subject.Targets[0].Details["generic_shape"]; shape != "abc-a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3 (40 characters)" {
		t.Fatalf("generic shape was not surfaced on the approval subject: %v", shape)
	}
	if loc := pending[0].ToolApproval.Plan.Presentation.Location; loc == nil ||
		loc.Origin != "in a tool call" || loc.Destination != "example.com" ||
		loc.OriginKind != api.ApprovalSecretOriginField {
		t.Fatalf("field location = %+v", loc)
	}

	secret, err := requestSecretApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "model_request"},
		SecretScreen: &hitl.SecretScreen{
			Surface: "model_request", CanRedact: true, DestinationID: "fireworks-main",
			RuleID:       "gitleaks:github-pat",
			RuleTitle:    "GitHub Personal Access Token",
			GenericShape: "abc-a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3 (40 characters)",
			Occurrences:  1, SourceKind: "tool_result", OriginKind: "field",
		},
	})
	testutil.FailErr(t, "request model secret approval", err)
	mgr.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	final, err := mgr.ResolveApprovalOption(ctx, sessionID, secret.CheckpointID, "send_redacted")
	testutil.FailErr(t, "resolve model secret redaction", err)
	if final.Status != hitl.DecisionStatusApproved || final.Result == nil || !final.Result.RedactSecrets {
		t.Fatalf("redacted final = %+v", final)
	}
}

func TestManagerUnrewritableSecretNamesHeldStandingRedaction(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)

	note := "Redaction is not offered here: replacing the value would run a command neither you nor the model wrote."
	_, err := requestSecretApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "command"},
		SecretScreen: &hitl.SecretScreen{
			Surface: "command", SurfaceLabel: "command",
			RedactionNote:         note,
			StandingRedactionHeld: true,
			DestinationID:         "proxy",
			RuleID:                "gitleaks:github-pat", RuleTitle: "GitHub Personal Access Token",
			GenericShape: "abc-a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3 (40 characters)",
			Occurrences:  1, SourceKind: "tool_argument", OriginKind: "field",
		},
	})
	testutil.FailErr(t, "request standing-redaction secret approval", err)
	pending, err := mgr.ListPending(ctx, sessionID, nil)
	testutil.FailErr(t, "list standing-redaction approval", err)
	if len(pending) != 1 {
		t.Fatalf("pending = %d", len(pending))
	}
	got := pending[0].ToolApproval.Plan.Presentation.OptionNote
	if got != note+" "+hitl.NoteStandingRedactionHeld {
		t.Fatalf("option note = %q — the card must say the held keep-redacting grant could not answer this ask", got)
	}
}

func TestManagerSecretReleaseQuietSendsUnredacted(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)

	secret, err := requestSecretApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "model_request", SessionID: sessionID},
		SecretScreen: &hitl.SecretScreen{
			Surface: "model_request", CanRedact: true, DestinationID: "fireworks-main",
			DestinationLabel: "Fireworks", RuleID: "gitleaks:github-pat",
			RuleTitle:    "GitHub Personal Access Token",
			GenericShape: "abc-a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3 (40 characters)",
			Occurrences:  1, SourceKind: "tool_result", OriginKind: "field",
		},
	})
	testutil.FailErr(t, "request quietable secret approval", err)
	pending, err := mgr.ListPending(ctx, sessionID, nil)
	testutil.FailErr(t, "list quietable approval", err)
	releaseID := ""
	for _, option := range pending[0].ToolApproval.Plan.Options {
		if option.Kind == api.ApprovalOptionKind(hitl.ApprovalOptionQuiet) && option.Rung == api.ApprovalOptionRungChat {
			releaseID = option.ID
		}
	}
	if releaseID == "" {
		t.Fatalf("secret card must offer the quiet slot: %+v", pending[0].ToolApproval.Plan.Options)
	}
	mgr.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	final, err := mgr.ResolveApprovalOption(ctx, sessionID, secret.CheckpointID, releaseID)
	testutil.FailErr(t, "resolve release quiet on redactable secret", err)
	if final.Result == nil || final.Result.RedactSecrets {
		t.Fatalf("stop-asking must send exactly what was asked: %+v", final.Result)
	}
}

func TestManagerSecretLocationFileOrigin(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)

	_, err := requestSecretApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{Tool: "model_request"},
		SecretScreen: &hitl.SecretScreen{
			Surface: "model_request", SurfaceLabel: "model request", CanRedact: true,
			DestinationID: "fireworks-main", DestinationLabel: "Fireworks",
			RuleID: "gitleaks:github-pat", RuleTitle: "GitHub Personal Access Token",
			GenericShape: "abc-a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3 (40 characters)",
			Occurrences:  1, SourceKind: "tool_result", SourceTool: "read",
			SourcePath: ".env.local", SourceLine: 7, OriginKind: "file",
			SourceToolCallID: "call_read_1",
		},
	})
	testutil.FailErr(t, "request file-origin secret approval", err)
	pending, err := mgr.ListPending(ctx, sessionID, nil)
	testutil.FailErr(t, "list file-origin approval", err)
	if len(pending) != 1 {
		t.Fatalf("pending = %d", len(pending))
	}
	loc := pending[0].ToolApproval.Plan.Presentation.Location
	if loc == nil || loc.Origin != ".env.local:7" || loc.Destination != "Fireworks" ||
		loc.OriginKind != api.ApprovalSecretOriginFile || loc.Path != ".env.local" ||
		loc.Line != 7 || loc.RevealToolCallID != "call_read_1" {
		t.Fatalf("location = %+v", loc)
	}
	if pending[0].ToolApproval.Plan.Subject.Targets[0].Details["source_path"] != nil {
		t.Fatalf("source path leaked onto the target: %+v", pending[0].ToolApproval.Plan.Subject.Targets[0].Details)
	}
}

type noopApprovalInstaller struct{}

func (noopApprovalInstaller) InstallApprovalOption(context.Context, string, hitl.ApprovalOption) (func(), error) {
	return func() {}, nil
}

func approveCurrentOption(t *testing.T, ctx context.Context, mgr *hitl.Manager, sessionID, checkpointID string) {
	t.Helper()
	mgr.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	_, err := mgr.ResolveApprovalOption(ctx, sessionID, checkpointID, "approve_current_action")
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
	replayed, err := mgr.ResolveApprovalOption(ctx, sessionID, resp.CheckpointID, "approve_current_action")
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

func TestManagerPublishCheckpointSSE(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "hitl-sse.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)

	projectDir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	sessionID := "sess-hitl-sse"
	insertSession(t, sqlDB, sessionID)

	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := hitl.NewManager(hitl.NewSQLStore(sqlDB), pub, authzcontext.SQLRecorder(sqlDB))

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: testdbseed.DefaultProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID,
		Kind:      api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{
			Tool:       "write",
			ProjectDir: projectDir,
		},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)
	assertCheckpointEvent(t, ch, resp.CheckpointID, api.CheckpointStatusPending)

	approveCurrentOption(t, ctx, mgr, sessionID, resp.CheckpointID)
	assertCheckpointEvent(t, ch, resp.CheckpointID, api.CheckpointStatusApproved)
}

// TestManagerCheckpointRoutesByProjectID uses the canonical project topic.
func TestManagerCheckpointRoutesByProjectID(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "hitl-route.db")

	const projectID = "11111111-2222-3333-4444-555555555555"
	sessionID := "sess-route"
	testdbseed.InsertSession(t, sqlDB, sessionID, projectID)

	hub := events.NewMemoryHub()
	mgr := hitl.NewManager(hitl.NewSQLStore(sqlDB), &events.Publisher{Hub: hub}, authzcontext.SQLRecorder(sqlDB))

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
			Tool:       "command",
			Args:       map[string]any{"command": "git push origin main"},
			ProjectDir: "/on/disk/project/path",
		},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)
	assertCheckpointEvent(t, ch, resp.CheckpointID, api.CheckpointStatusPending)
}

// TestManagerContentApplyRoutesByProjectID uses the canonical project topic.
func TestManagerContentApplyRoutesByProjectID(t *testing.T) {
	ctx := context.Background()
	sqlDB := testdbfixture.Open(t, "hitl-ca-route.db")

	const projectID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	sessionID := "sess-ca-route"
	testdbseed.InsertSession(t, sqlDB, sessionID, projectID)

	hub := events.NewMemoryHub()
	mgr := hitl.NewManager(hitl.NewSQLStore(sqlDB), &events.Publisher{Hub: hub}, authzcontext.SQLRecorder(sqlDB))

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

func newTestManager(t *testing.T) (db.ReadHandle, *hitl.Manager, string) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "hitl.db")
	hub := events.NewMemoryHub()
	pub := &events.Publisher{Hub: hub}
	mgr := hitl.NewManager(hitl.NewSQLStore(sqlDB), pub, authzcontext.SQLRecorder(sqlDB))
	return sqlDB, mgr, "sess-hitl-1"
}

func insertSession(t *testing.T, sqlDB db.Handle, sessionID string) {
	t.Helper()
	testdbseed.InsertSession(t, sqlDB, sessionID, testdbseed.DefaultProjectID)
}

// TestCheckpointAutoExpiresToDenied verifies pending checkpoint expiry.
func TestCheckpointAutoExpiresToDenied(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	mgr.SetCheckpointExpiry(func() time.Duration { return 15 * time.Millisecond })

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:      sessionID,
		Kind:           api.CheckpointKindToolApproval,
		Type:           hitl.DecisionTypeApprove,
		Title:          "Approve command",
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo hi"}},
	})
	testutil.FailErr(t, "mgr.RequestCheckpoint failed", err)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		final, perr := mgr.PollCheckpoint(ctx, resp.CheckpointID)
		testutil.FailErr(t, "PollCheckpoint", perr)
		if final.Status == hitl.DecisionStatusPending {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if final.Status != hitl.DecisionStatusExpired {
			t.Fatalf("status = %q, want expired (fail-safe deny)", final.Status)
		}
		denied, derr := mgr.SessionApprovalDenied(ctx, sessionID)
		testutil.FailErr(t, "SessionApprovalDenied", derr)
		if !denied {
			t.Fatal("expired checkpoint must count as a denied approval")
		}
		return
	}
	t.Fatal("pending checkpoint never expired — timeout not wired")
}

func TestCheckpointExpiryNotifiesToolApprovalTerminal(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	mgr.SetCheckpointExpiry(func() time.Duration { return 15 * time.Millisecond })

	var gotChat, gotKey string
	var gotStatus hitl.DecisionStatus
	var calls int
	mgr.SetToolApprovalTerminalHook(func(chat, key string, status hitl.DecisionStatus) {
		calls++
		gotChat, gotKey, gotStatus = chat, key, status
	})

	const grantKey = "command\x00\x00{\"command\":\"echo hi\"}"
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:        sessionID,
		Kind:             api.CheckpointKindToolApproval,
		Type:             hitl.DecisionTypeApprove,
		Title:            "Approve command",
		ProposedAction:   &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo hi"}},
		JoinedCount:      1,
		CoalesceChat:     sessionID,
		CoalesceGrantKey: grantKey,
	})
	testutil.FailErr(t, "RequestCheckpoint", err)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		final, perr := mgr.PollCheckpoint(ctx, resp.CheckpointID)
		testutil.FailErr(t, "PollCheckpoint", perr)
		if final.Status == hitl.DecisionStatusPending {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if final.Status != hitl.DecisionStatusExpired {
			t.Fatalf("status = %q want expired", final.Status)
		}
		if calls != 1 {
			t.Fatalf("terminal hook calls = %d want 1", calls)
		}
		if gotChat != sessionID || gotKey != grantKey {
			t.Fatalf("hook got chat=%q key=%q", gotChat, gotKey)
		}
		if gotStatus != hitl.DecisionStatusExpired {
			t.Fatalf("hook status = %q want expired", gotStatus)
		}
		return
	}
	t.Fatal("pending checkpoint never expired")
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

func TestRestorePendingExpiresFromOriginalCreationTime(t *testing.T) {
	ctx := context.Background()
	sqlDB, original, sessionID := newTestManager(t)
	insertSession(t, sqlDB, sessionID)
	original.SetCheckpointExpiry(func() time.Duration { return 0 })
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, original, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		Type: hitl.DecisionTypeApprove, Title: "Old approval",
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo old"}},
	})
	testutil.FailErr(t, "create pending checkpoint", err)
	_, err = sqlDB.ExecContext(ctx, "UPDATE checkpoints SET created_at = ? WHERE id = ?", db.FormatTime(time.Now().UTC().Add(-time.Hour)), resp.CheckpointID)
	testutil.FailErr(t, "age pending checkpoint", err)

	restarted := hitl.NewManager(hitl.NewSQLStore(sqlDB), nil, authzcontext.SQLRecorder(sqlDB))
	restarted.SetCheckpointExpiry(func() time.Duration { return 15 * time.Minute })
	restored := 0
	restarted.SetToolApprovalRestoreHook(func(hitl.StoredCheckpoint) { restored++ })
	testutil.FailErr(t, "restore pending checkpoints", restarted.RestorePending(ctx))
	if restored != 0 {
		t.Fatalf("already-expired checkpoint restored into coalescing: %d", restored)
	}
	final, err := restarted.PollCheckpoint(ctx, resp.CheckpointID)
	testutil.FailErr(t, "poll restored checkpoint", err)
	if final.Status != hitl.DecisionStatusExpired {
		t.Fatalf("status = %q want expired", final.Status)
	}
}

func TestRestorePendingRebuildsCoalescingBeforeExpiry(t *testing.T) {
	ctx := context.Background()
	sqlDB, original, sessionID := newTestManager(t)
	insertSession(t, sqlDB, sessionID)
	original.SetCheckpointExpiry(func() time.Duration { return 0 })
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, original, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		Type: hitl.DecisionTypeApprove, Title: "Restored approval",
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo restored"}},
		CoalesceChat:   sessionID, CoalesceGrantKey: "restored-key",
	})
	testutil.FailErr(t, "create pending checkpoint", err)

	restarted := hitl.NewManager(hitl.NewSQLStore(sqlDB), nil, authzcontext.SQLRecorder(sqlDB))
	restarted.SetCheckpointExpiry(func() time.Duration { return 2 * time.Second })
	order := make(chan string, 2)
	restarted.SetToolApprovalRestoreHook(func(row hitl.StoredCheckpoint) {
		if row.ID != resp.CheckpointID {
			t.Fatalf("restored checkpoint = %q", row.ID)
		}
		order <- "restore"
	})
	restarted.SetToolApprovalTerminalHook(func(_, _ string, _ hitl.DecisionStatus) { order <- "terminal" })
	testutil.FailErr(t, "restore pending checkpoints", restarted.RestorePending(ctx))
	if got := <-order; got != "restore" {
		t.Fatalf("first hook = %q want restore", got)
	}
	select {
	case got := <-order:
		if got != "terminal" {
			t.Fatalf("second hook = %q want terminal", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restored checkpoint did not expire")
	}
}

func TestRestoreRejectedToolApprovalDenials(t *testing.T) {
	sqlDB, original, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, original, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		Type: hitl.DecisionTypeApprove, Title: "Denied approval",
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo denied"}},
		CoalesceChat:   sessionID, CoalesceGrantKey: "denied-key",
	})
	testutil.FailErr(t, "create checkpoint", err)
	_, err = original.ResolveCheckpoint(ctx, sessionID, resp.CheckpointID, api.CheckpointKindToolApproval,
		&hitl.DecisionResult{Approved: false}, nil)
	testutil.FailErr(t, "reject checkpoint", err)

	restarted := hitl.NewManager(hitl.NewSQLStore(sqlDB), nil, authzcontext.SQLRecorder(sqlDB))
	var restored []hitl.StoredCheckpoint
	restarted.SetToolApprovalDenyRestoreHook(func(row hitl.StoredCheckpoint) { restored = append(restored, row) })
	testutil.FailErr(t, "restore rejected tool approvals", restarted.RestoreRejectedToolApprovalDenials(ctx))
	if len(restored) != 1 || restored[0].ID != resp.CheckpointID {
		t.Fatalf("restored denials = %+v", restored)
	}

	// A visible user intent after the denial is the boundary that clears the
	// deny set, so a restart after it restores nothing for this chat.
	insertUserIntentMessage(t, sqlDB, sessionID, "msg-after-denial", time.Now().Add(time.Second))
	afterBoundary := hitl.NewManager(hitl.NewSQLStore(sqlDB), nil, authzcontext.SQLRecorder(sqlDB))
	restored = nil
	afterBoundary.SetToolApprovalDenyRestoreHook(func(row hitl.StoredCheckpoint) { restored = append(restored, row) })
	testutil.FailErr(t, "restore after intent boundary", afterBoundary.RestoreRejectedToolApprovalDenials(ctx))
	if len(restored) != 0 {
		t.Fatalf("denial restored across an intent boundary: %+v", restored)
	}
}

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

func TestPatchPendingAIRationaleNoSSEAfterResolve(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "hitl-rationale.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)

	const projectID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	sessionID := "sess-rationale"
	testdbseed.InsertSession(t, sqlDB, sessionID, projectID)

	hub := events.NewMemoryHub()
	mgr := hitl.NewManager(hitl.NewSQLStore(sqlDB), &events.Publisher{Hub: hub}, authzcontext.SQLRecorder(sqlDB))

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: projectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:      sessionID,
		Kind:           api.CheckpointKindToolApproval,
		ProjectID:      projectID,
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "git push"}},
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

	row, err := mgr.Store().Get(ctx, resp.CheckpointID)
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
	mgr := hitl.NewManager(hitl.NewSQLStore(sqlDB), &events.Publisher{Hub: hub}, authzcontext.SQLRecorder(sqlDB))

	ch, unsub, err := hub.Subscribe(ctx, events.Subscription{Project: projectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "hub.Subscribe failed", err)
	defer unsub()

	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:          sessionID,
		Kind:               api.CheckpointKindToolApproval,
		ProjectID:          projectID,
		ProposedAction:     &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "git push"}},
		AIRationalePending: true,
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	assertCheckpointEvent(t, ch, resp.CheckpointID, api.CheckpointStatusPending)

	// The reserved-slot flag is surfaced on the initial pending event.
	row, err := mgr.Store().Get(ctx, resp.CheckpointID)
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

func TestResolveCheckpointGrantBlockedWhenAuthzLedgerFails(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "hitl-seal-fail.db")
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	sessionID := "sess-hitl-1"
	insertSession(t, sqlDB, sessionID)
	mgr := hitl.NewManager(hitl.NewSQLStore(sqlDB), &events.Publisher{Hub: events.NewMemoryHub()},
		authzcontext.LedgerRecorder{Ledger: &authzcontext.Ledger{Store: authzcontext.FailStore{}}})
	resp, err := requestExplicitApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID:      sessionID,
		Kind:           api.CheckpointKindToolApproval,
		Type:           hitl.DecisionTypeApprove,
		Title:          "Approve command",
		ProposedAction: &hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "echo hi"}},
	})
	testutil.FailErr(t, "RequestCheckpoint", err)
	mgr.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	_, err = mgr.ResolveApprovalOption(ctx, sessionID, resp.CheckpointID, "approve_current_action")
	if err == nil || !errors.Is(err, authzledger.ErrSealFailed) {
		t.Fatalf("want ErrSealFailed, got %v", err)
	}
	final, err := mgr.PollCheckpoint(ctx, resp.CheckpointID)
	testutil.FailErr(t, "PollCheckpoint", err)
	if final.Status != hitl.DecisionStatusPending {
		t.Fatalf("approve must not apply when ledger append fails; status = %q", final.Status)
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
			Tool:    "command",
			Command: "git status",
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
