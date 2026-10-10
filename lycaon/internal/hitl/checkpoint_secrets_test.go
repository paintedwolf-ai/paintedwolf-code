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

func TestManagerSecretRedactionRequiresHostCapability(t *testing.T) {
	sqlDB, mgr, sessionID := newTestManager(t)
	ctx := testdbseed.OwnerCaller(t, context.Background(), sqlDB)
	insertSession(t, sqlDB, sessionID)

	_, err := requestSecretApprovalCheckpoint(t, ctx, mgr, hitl.CheckpointRequest{
		SessionID: sessionID, Kind: api.CheckpointKindToolApproval,
		ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "fetch_url",
},
},
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
	mgr.Authority.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	_, err = mgr.Authority.ResolveApprovalOption(ctx, sessionID, pending[0].ID, "send_redacted")
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
		ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "model_request",
},
},
		SecretScreen: &hitl.SecretScreen{
			Surface: "model_request", CanRedact: true, DestinationID: "fireworks-main",
			RuleID:       "gitleaks:github-pat",
			RuleTitle:    "GitHub Personal Access Token",
			GenericShape: "abc-a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3a1b2c3 (40 characters)",
			Occurrences:  1, SourceKind: "tool_result", OriginKind: "field",
		},
	})
	testutil.FailErr(t, "request model secret approval", err)
	mgr.Authority.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	final, err := mgr.Authority.ResolveApprovalOption(ctx, sessionID, secret.CheckpointID, "send_redacted")
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
		ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
},
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
		ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "model_request",
},
Scope: hitl.ActionScope{
SessionID: sessionID,
},
},
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
	mgr.Authority.SetApprovalAuthorityInstaller(noopApprovalInstaller{})
	final, err := mgr.Authority.ResolveApprovalOption(ctx, sessionID, secret.CheckpointID, releaseID)
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
		ProposedAction: &hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "model_request",
},
},
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
