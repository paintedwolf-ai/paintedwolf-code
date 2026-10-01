package prompts

// MergeModelCapabilityVars exposes active-model modality flags for declarative template gating.
func MergeModelCapabilityVars(vision bool, into map[string]any) {
	if into == nil {
		return
	}
	caps, _ := into["caps"].(map[string]any)
	if caps == nil {
		caps = map[string]any{}
	}
	caps["vision"] = vision
	into["caps"] = caps
}
