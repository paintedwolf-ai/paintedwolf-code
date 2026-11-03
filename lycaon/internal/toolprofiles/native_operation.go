package toolprofiles

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/platform"
)

// evaluateNativeOperation retains live profile restrictions without requiring a
// retired structured schema. Active-catalog selection and binding precede this
// policy; this check cannot supply an effect permit or bypass the owner floor.
func (p *ProfilePolicyEngine) evaluateNativeOperation(ctx context.Context, profileID string, eval platform.PolicyContext) (*platform.PolicyDecision, error) {
	bound := *eval.NativeOperation
	definition := bound.Definition()
	facts := definition.Facts()
	invocation := bound.Invocation()
	if definition.Digest() == "" || facts.Identity != eval.ToolName || invocation.SessionID != eval.SessionID || invocation.Scope.ProjectID != eval.ProjectID || invocation.Identity.ExecutionID != eval.ActionID {
		return nil, fmt.Errorf("native policy attribution does not match bound operation")
	}
	allowed, err := p.CommandOperationAllowed(ctx, profileID, facts.Identity, bound.Command())
	if err != nil {
		return nil, err
	}
	code := "COMMAND_OPERATION_NOT_ALLOWED"
	if allowed {
		effects, err := p.CommandEffects(ctx, profileID)
		if err != nil {
			return nil, err
		}
		if effects.Allows(bound.Effects()) {
			return &platform.PolicyDecision{Allowed: true}, nil
		}
		code = "COMMAND_EFFECT_NOT_ALLOWED"
	}
	return &platform.PolicyDecision{Blocked: true, RejectCode: code, RejectData: map[string]any{"tool": facts.Identity, "owner_ref": facts.Contract.Owner, "permission_profile": profileID, "expected_form": "", "source_start_byte": invocation.Span.StartByte, "source_end_byte": invocation.Span.EndByte, "source_line": invocation.Span.Line, "source_column": invocation.Span.Column, "source_unit_id": invocation.SourceUnitID}}, nil
}
