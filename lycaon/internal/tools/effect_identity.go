package tools

// EffectActionID identifies the reached native child without replacing the
// actual initiating model call used by transcript/checkpoint correlation.
func (tc ToolContext) EffectActionID() string {
	if tc.Invocation.NativeOperation != nil {
		return tc.Invocation.NativeOperation.Invocation().Identity.ExecutionID
	}
	return tc.Identity.ToolCallID
}
