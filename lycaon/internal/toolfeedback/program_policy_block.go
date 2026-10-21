package toolfeedback

import (
	"context"
	"fmt"
	"maps"
	"math"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// ProgramPolicyBlock preserves an actual preinvoke policy occurrence until its
// receipt settles. It retains no submitted arguments or whole fact activation.
// The initiating frame's state lease must survive settlement and delivery.
type ProgramPolicyBlock struct {
	decision                  oar.Decision
	anchor                    string
	reject                    toolrejection.ToolReject
	invocation                toolcontract.OperationInvocation
	definition, command, mode string
}

func (b *ProgramPolicyBlock) Error() string { return b.reject.Error() }
func (b *ProgramPolicyBlock) Unwrap() error { return &b.reject }

func freezeProgramPolicyBlock(ctx context.Context, anchor string, decision *oar.Decision, operation *toolcontract.BoundOperation, retain func(context.Context, int64) error) error {
	if decision == nil || decision.Effect != oar.EffectBlock || decision.Code == "" || retain == nil {
		return fmt.Errorf("program policy requires actual block and frame retention")
	}
	// Include retained strings, the copy map and bounded rendered-copy storage.
	// No copy allocation precedes admission. The host frame releases it after
	// all delivery/owner borrows drain; the occurrence never acquires authority.
	bytes := int64(64 << 10)
	add := func(value string) bool {
		if int64(len(value)) > (math.MaxInt64-bytes-512)/32 {
			return false
		}
		bytes += int64(len(value))*32 + 512
		return true
	}
	for _, value := range []string{anchor, decision.Code, decision.Rule} {
		if !add(value) {
			return fmt.Errorf("program policy retention overflow")
		}
	}
	var invocation toolcontract.OperationInvocation
	var definition, command, mode string
	if operation != nil {
		invocation = operation.Invocation()
		definition, command, mode = operation.Definition().Digest(), operation.Command(), operation.Mode()
		for _, value := range []string{definition, command, mode, string(invocation.Kind), invocation.ModelToolCallID, invocation.ActorID, invocation.SessionID, invocation.SourceUnitID, invocation.CatalogDigest, invocation.Identity.ProgramID, invocation.Identity.NodeID, invocation.Identity.ExecutionID, invocation.Identity.ParentExecutionID, string(invocation.Scope.Kind), invocation.Scope.ProjectID, invocation.Scope.RootID, invocation.Scope.BranchID, invocation.Scope.Directory} {
			if !add(value) {
				return fmt.Errorf("program policy attribution overflow")
			}
		}
	}
	for key, value := range decision.Copy {
		if !add(key) || !add(value) {
			return fmt.Errorf("program policy retention overflow")
		}
	}
	if err := retain(ctx, bytes); err != nil {
		return err
	}
	copy := make(map[string]string, len(decision.Copy))
	for key, value := range decision.Copy {
		copy[strings.Clone(key)] = strings.Clone(value)
	}
	code, rule := strings.Clone(decision.Code), strings.Clone(decision.Rule)
	return &ProgramPolicyBlock{invocation: invocation, definition: definition, command: command, mode: mode, anchor: strings.Clone(anchor), decision: oar.Decision{Effect: oar.EffectBlock, Code: code, Rule: rule, Copy: copy}, reject: toolrejection.ToolReject{Code: code, Data: map[string]any{"policy_rule": rule, "policy_anchor": anchor}}}
}

// MatchesOperation binds frozen policy copy to the same actual dynamic child.
func (b *ProgramPolicyBlock) MatchesOperation(operation toolcontract.BoundOperation) bool {
	return b != nil && b.invocation == operation.Invocation() && b.definition == operation.Definition().Digest() && b.command == operation.Command() && b.mode == operation.Mode()
}

// RenderSettled uses the frozen occurrence copy with admitted receipt details.
// It never re-runs conditions, on-fire actions, or current execution admission.
func (b *ProgramPolicyBlock) RenderSettled(ctx context.Context, renderer *oar.Renderer, reject *toolrejection.ToolReject) error {
	if b == nil || reject == nil || reject.Code != b.reject.Code {
		return fmt.Errorf("settled policy feedback changed rejection identity")
	}
	decision := b.decision
	decision.Data = reject.Data
	decision.Copy = maps.Clone(b.decision.Copy)
	rendered, err := renderer.Render(ctx, oar.StageFromAnchor(b.anchor), &decision)
	if err != nil {
		return err
	}
	if block, ok := oar.FirstBlock(rendered); ok && block.Text != "" {
		return guidance.NewRefusal(reject.Code, block.Text).WithPolicyCopy(decision.Copy).WithDetails(reject.Data, nil).WithCause(reject)
	}
	return toolrejection.RenderReject(reject, nil)
}
