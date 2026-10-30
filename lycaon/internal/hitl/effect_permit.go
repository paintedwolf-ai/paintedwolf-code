package hitl

import "strings"

// EffectPermitIdentity keeps one-action authority separate from reusable semantic
// grants. ExecutionID is empty only for retained structured invocations. It never
// replaces the actual initiating model ToolCallID.
type EffectPermitIdentity struct {
	SessionID   string
	ToolCallID  string
	ExecutionID string
}

func (i EffectPermitIdentity) Valid() bool {
	return strings.TrimSpace(i.SessionID) != "" && strings.TrimSpace(i.ToolCallID) != "" && strings.TrimSpace(i.ExecutionID) == i.ExecutionID &&
		!strings.ContainsAny(i.SessionID+i.ToolCallID+i.ExecutionID, "\x00\x01")
}

// Key is an ephemeral permit key, not the stable semantic GrantKey.
func (i EffectPermitIdentity) Key() string {
	return strings.TrimSpace(i.SessionID) + "\x01" + strings.TrimSpace(i.ToolCallID) + "\x01" + i.ExecutionID
}

func (d ApprovalAuthorityDelta) PermitIdentity() EffectPermitIdentity {
	return EffectPermitIdentity{SessionID: d.SessionID, ToolCallID: d.ToolCallID, ExecutionID: d.ExecutionID}
}
