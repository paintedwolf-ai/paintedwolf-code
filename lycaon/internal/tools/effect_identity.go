package tools

import "github.com/lycaon/lycaon/internal/hitl"

func (tc ToolContext) PermitIdentity() hitl.EffectPermitIdentity {
	identity := hitl.EffectPermitIdentity{SessionID: tc.Identity.SessionID, ToolCallID: tc.Identity.ToolCallID}
	if tc.Invocation.NativeOperation != nil {
		identity.ExecutionID = tc.Invocation.NativeOperation.Invocation().Identity.ExecutionID
	}
	return identity
}

// EffectActionID identifies the reached native child without replacing the
// actual initiating model call used by transcript/checkpoint correlation.
func (tc ToolContext) EffectActionID() string {
	if tc.Invocation.NativeOperation != nil {
		return tc.Invocation.NativeOperation.Invocation().Identity.ExecutionID
	}
	return tc.Identity.ToolCallID
}
