package wiring

import (
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// authzEventsByAction lists the session's authz ledger rows for one action.
func authzEventsByAction(t *testing.T, h *Harness, sessionID string, action authzcontext.EventAction) []authzcontext.Event {
	t.Helper()
	rows, err := authzcontext.NewSQLStore(h.DB).ListEvents(t.Context(), sessionID)
	testutil.FailErr(t, "list authz events", err)
	var out []authzcontext.Event
	for _, row := range rows {
		if row.Action == action {
			out = append(out, row)
		}
	}
	return out
}

// Checkpoint resolutions through the built app seal the approval, capability
// and human gates in the authz ledger the app wires.
func TestAuthzEventHookSites(t *testing.T) {
	h := BuildForTest(t)
	ctx := h.OwnerCtx(t, t.Context())
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, h.ProjectDir(t, "authz-hooks"))
	testutil.FailErr(t, "create session", err)

	_, decision := gate.Evaluate(gate.Facts{
		Stage: gate.StagePreSpawn, Ran: gate.ProducerApprovalRequest,
		ApprovalRequest: &gate.ApprovalRequest{Count: 1},
	}, gate.DefaultPosture)
	approval, err := h.Sessions.Checkpoints.RequestCheckpoint(ctx, hitl.CheckpointRequest{
		SessionID:  sess.ID,
		Kind:       wire.CheckpointKindToolApproval,
		ToolCallID: "call-detection",
		Decision:   decision,
		ProposedAction: &hitl.ProposedAction{
			Invocation: hitl.ActionInvocation{
				Tool: "command", Args: map[string]any{"command": "aws iam create-user"}, ActionID: "call-detection",
			},
			Scope: hitl.ActionScope{SessionID: sess.ID},
		},
		Detection: &hitl.DetectionMatch{PackID: "pack-cloud", RuleID: "rule-42", RuleTitle: "IAM principal creation", Level: "high"},
		DetectionEndpoints: []authzledger.CapabilityEndpoint{{
			Host: "iam.amazonaws.com", Port: 443, Transport: "tcp", Attempts: 1,
		}},
	})
	testutil.FailErr(t, "request tool approval", err)
	if got := authzEventsByAction(t, h, sess.ID, authzcontext.EventActionApprovalDecision); len(got) != 0 {
		t.Fatalf("pending approval sealed rows before resolution: %+v", got)
	}
	_, err = h.Sessions.Checkpoints.ResolveCheckpoint(ctx, sess.ID, approval.CheckpointID, wire.CheckpointKindToolApproval,
		&hitl.DecisionResult{Approved: false}, nil)
	testutil.FailErr(t, "deny tool approval", err)

	decisions := authzEventsByAction(t, h, sess.ID, authzcontext.EventActionApprovalDecision)
	if len(decisions) != 1 || decisions[0].Outcome != authzcontext.EventOutcomeDenied || decisions[0].ResolvedBy != authzcontext.ResolvedByHuman {
		t.Fatalf("approval_decision rows = %+v", decisions)
	}
	detections := authzEventsByAction(t, h, sess.ID, authzcontext.EventActionDetectionResolved)
	if len(detections) != 1 || detections[0].Outcome != authzcontext.EventOutcomeDenied || detections[0].ToolName != "command" {
		t.Fatalf("detection_resolved rows = %+v", detections)
	}

	before := "old\n"
	apply, err := h.Sessions.Checkpoints.RequestCheckpoint(ctx, hitl.CheckpointRequest{
		SessionID: sess.ID,
		Kind:      wire.CheckpointKindContentApply,
		ContentApply: &hitl.ContentApplyPayload{
			Tool: "write", ToolCallID: "call-apply", Path: "notes.md", Before: &before, After: "new\n",
		},
	})
	testutil.FailErr(t, "request content apply", err)
	_, err = h.Sessions.Checkpoints.ResolveCheckpoint(ctx, sess.ID, apply.CheckpointID, wire.CheckpointKindContentApply, nil,
		&hitl.ContentApplyResolve{Decision: wire.ContentApplyReject})
	testutil.FailErr(t, "reject content apply", err)
	applied := authzEventsByAction(t, h, sess.ID, authzcontext.EventActionContentApplyResolved)
	if len(applied) != 1 || applied[0].Outcome != authzcontext.EventOutcomeDenied || applied[0].ResolvedBy != authzcontext.ResolvedByHuman {
		t.Fatalf("content_apply_resolved rows = %+v", applied)
	}
}

func authzVerifyMockClient() *llm.MockProvider {
	return llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "Done."}}})
}

// The built app seals an authorization context for each prompt it runs.
func TestBuildSessionWiresAuthzCapturer(t *testing.T) {
	h := BuildForTest(t, WithLLMClient(authzVerifyMockClient()))
	if !h.Sessions.Manager.Runner.Authorization.Wired() {
		t.Fatal("built app left the authorization sealer unwired")
	}
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, h.ProjectDir(t, "authz-seal"))
	testutil.FailErr(t, "create session", err)
	contexts, err := authzcontext.NewSQLStore(h.DB).ListContexts(t.Context(), sess.ID)
	testutil.FailErr(t, "list contexts before prompt", err)
	if len(contexts) != 0 {
		t.Fatalf("contexts before any prompt = %d", len(contexts))
	}
	_, err = h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "hello")
	testutil.FailErr(t, "prompt", err)
	contexts, err = authzcontext.NewSQLStore(h.DB).ListContexts(t.Context(), sess.ID)
	testutil.FailErr(t, "list contexts after prompt", err)
	if len(contexts) != 1 || contexts[0].SessionID != sess.ID {
		t.Fatalf("sealed contexts after one prompt = %+v", contexts)
	}
}

// A seal failure refuses the turn and queues the authz.seal_failed inform.
func TestAuthzSealFailureSurfacesViaNudge(t *testing.T) {
	h := BuildForTest(t, WithLLMClient(authzVerifyMockClient()))
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, h.ProjectDir(t, "authz-seal-fail"))
	testutil.FailErr(t, "create session", err)
	want := anchor.InformRender(anchor.AuthzSealFailed)
	if want == "" {
		t.Fatal("anchor registry has no authz.seal_failed inform")
	}
	if pending := h.Sessions.Manager.Coordinator.Guidance.PendingIDs(t.Context(), sess.ID); slices.Contains(pending, want) {
		t.Fatalf("seal failure queued before any prompt: %v", pending)
	}
	// A sealer without a store fails every seal.
	h.Sessions.Manager.Runner.Authorization.SetSealer(&authzcontext.Sealer{})
	_, err = h.Sessions.Manager.Submissions.Prompt(t.Context(), sess.ID, "hello")
	if !errors.Is(err, authzledger.ErrSealFailed) {
		t.Fatalf("prompt with failing sealer err = %v, want ErrSealFailed", err)
	}
	if pending := h.Sessions.Manager.Coordinator.Guidance.PendingIDs(t.Context(), sess.ID); !slices.Contains(pending, want) {
		t.Fatalf("pending guidance = %v, want %q", pending, want)
	}
}
