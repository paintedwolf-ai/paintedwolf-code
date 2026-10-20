package toolexecution

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

type programEffectPolicy struct{ effects toolcontract.CommandEffects }

func (p programEffectPolicy) Evaluate(context.Context, platform.PolicyContext) (*platform.PolicyDecision, error) {
	return &platform.PolicyDecision{Allowed: true}, nil
}
func (p programEffectPolicy) FreezeCommandProfile(ctx context.Context, _ string) (context.Context, toolcontract.CommandEffects, error) {
	return ctx, p.effects, nil
}

func (programEffectPolicy) CommandOperationAllowed(context.Context, string, string, string) (bool, error) {
	return true, nil
}

func TestBoundProgramModePreservesProfileAndSourceInEffectRefusal(t *testing.T) {
	registry := tools.NewDefaultRegistry()
	for _, name := range []string{"read", "write"} {
		contract, _ := toolcontract.Lookup(name)
		testutil.FailErr(t, "register actual owner declaration", registry.RegisterDefinition(tools.Definition{Meta: tools.ToolMeta{Name: name, ArgsSchema: map[string]any{"type": "object"}}, Contract: contract, Handler: func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			t.Fatal("admission executed owner")
			return "", nil
		}}))
	}
	invocation := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: "child"}, ActorID: "principal", SessionID: "session", Scope: toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope}, SourceUnitID: "source", Span: toolcontract.SourceSpan{StartByte: 4, EndByte: 8, Line: 2, Column: 3}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	readonly, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	read, _ := registry.Definition("read")
	write, _ := registry.Definition("write")
	for _, profile := range []string{"worker_readonly", "explore_readonly", "plan_review_readonly", "observe_investigate", "observe_plan", "observe_synthesis", "spec"} {
		t.Run(profile, func(t *testing.T) {
			executor := NewExecutor(programEffectPolicy{effects: readonly}, registry, profile)
			surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: profile}, []toolcontract.OperationDefinition{read.Operation, write.Operation})
			testutil.FailErr(t, "freeze read-only operation surface", err)
			readMode := read.Operation.Facts().Commands[0].Effects[0].Mode
			testutil.FailErr(t, "admit read-only catalog mode", surface.ValidateBound(read.Operation, invocation, "cat", readMode, nil))
			writeMode := write.Operation.Facts().Commands[0].Effects[0].Mode
			err = surface.ValidateBound(write.Operation, invocation, "tee", writeMode, nil)
			var rejected *toolrejection.ToolReject
			if !errors.As(err, &rejected) || rejected.Code != "COMMAND_EFFECT_NOT_ALLOWED" || rejected.OwnerRef != write.Contract.Owner || rejected.Data["permission_profile"] != profile || rejected.Data["source_start_byte"] != 4 {
				t.Fatalf("lost operation/profile/source refusal: %+v", err)
			}
			if err := surface.ValidateBound(write.Operation, invocation, "tee", "invented-mode", nil); err == nil {
				t.Fatal("invented mode admitted")
			}
		})
	}
}

func TestProgramSurfaceFreezesProductionProfileAndWorkerOverlayTogether(t *testing.T) {
	readOnly, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	all, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation)
	boundary := sandbox.NewBoundary(sandbox.Config{}, nil)
	lookups := 0
	boundary.SetProfileSource(func(_ context.Context, sessionID string) []sandbox.ToolProfile {
		lookups++
		if sessionID != "worker-session" {
			t.Fatalf("lost worker session %q", sessionID)
		}
		if lookups > 1 {
			return []sandbox.ToolProfile{{ID: "fixture", Tools: map[string]bool{"read": true, "write": true}, CommandEffects: all}}
		}
		return []sandbox.ToolProfile{{ID: "fixture", Tools: map[string]bool{"read": true}, DenyTools: []string{"write"}, CommandEffects: readOnly}}
	})
	registry := tools.NewDefaultRegistry()
	for _, name := range []string{"read", "write"} {
		contract, _ := toolcontract.Lookup(name)
		testutil.FailErr(t, "register frozen owner", registry.RegisterDefinition(tools.Definition{Meta: tools.ToolMeta{Name: name, ArgsSchema: map[string]any{"type": "object"}}, Contract: contract, Handler: func(context.Context, map[string]any, tools.ToolContext) (string, error) {
			t.Fatal("profile capture dispatched handler")
			return "", nil
		}}))
	}
	executor := NewExecutor(NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), nil), registry, "fixture")
	ctx := sandbox.WithSessionID(t.Context(), "worker-session")
	surface, err := executor.Metadata.FreezeProgramSurface(ctx, platform.ToolFilter{ProfileID: "fixture"}, []toolcontract.OperationDefinition{nativeDefinition(t, "read"), nativeDefinition(t, "write")})
	testutil.FailErr(t, "freeze production approval/profile boundary", err)
	if lookups != 1 || !surface.allowed.Allows(readOnly) || surface.allowed.Allows(all) {
		t.Fatalf("profile capture changed between effect and exposure: lookups=%d classes=%v", lookups, surface.allowed.Classes())
	}
	write, _ := registry.Definition("write")
	if err := surface.selection.offered.Validate(write.Operation, toolcontract.OperationInvocation{}); err == nil {
		t.Fatal("later overlay leaked hidden write into frozen surface")
	}
}

func TestCoordinatorCommandSurfaceIntersectsActualPostureDeclaration(t *testing.T) {
	all, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation, toolcontract.CommandExternalExec, toolcontract.CommandNetworkEgress)
	executor := NewExecutor(programEffectPolicy{effects: all}, tools.NewDefaultRegistry(), "coordinator")
	read, write := nativeDefinition(t, "read"), nativeDefinition(t, "write")
	invocation, _ := frameFixture(t)
	for _, surfaceID := range []string{"observe_investigate", "observe_plan", "observe_synthesis"} {
		t.Run(surfaceID, func(t *testing.T) {
			surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: "coordinator", SurfaceID: surfaceID}, []toolcontract.OperationDefinition{read, write})
			testutil.FailErr(t, "capture actual read-only posture", err)
			testutil.FailErr(t, "admit native read", surface.ValidateBound(read, invocation, "cat", read.Facts().Commands[0].Effects[0].Mode, nil))
			if err := surface.ValidateBound(write, invocation, "tee", write.Facts().Commands[0].Effects[0].Mode, nil); err == nil {
				t.Fatal("coordinator profile widened read-only posture")
			}
		})
	}
	for _, surfaceID := range []string{"", "invented-surface"} {
		if _, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: "coordinator", SurfaceID: surfaceID}, []toolcontract.OperationDefinition{read}); err == nil {
			t.Fatal("missing coordinator posture admitted")
		}
	}
}
