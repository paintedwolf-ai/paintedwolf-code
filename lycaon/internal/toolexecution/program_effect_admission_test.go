package toolexecution

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

type programEffectPolicy struct{ effects toolcontract.CommandEffects }

func (p programEffectPolicy) Evaluate(context.Context, platform.PolicyContext) (*platform.PolicyDecision, error) {
	return &platform.PolicyDecision{Allowed: true}, nil
}
func (p programEffectPolicy) CommandEffects(context.Context, string) (toolcontract.CommandEffects, error) {
	return p.effects, nil
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
			surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: profile}, []string{"read", "write"})
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
