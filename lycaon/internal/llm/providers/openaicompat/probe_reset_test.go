package openaicompat

// ResetReasoningProbeForTest clears probe cache (tests only).
func ResetReasoningProbeForTest() {
	reasoningUnsupported.Clear()
}
