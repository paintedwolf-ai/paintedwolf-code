package discovery

import "github.com/lycaon/lycaon/internal/llm/modelinfo"

func ollamaThinking(capabilities []string, model string) *modelinfo.ThinkingCapabilities {
	switch ListAnyEvidence(capabilities, "thinking") {
	case modelinfo.CapabilityUnsupported:
		return &modelinfo.ThinkingCapabilities{State: "unsupported"}
	case modelinfo.CapabilitySupported:
		// Family declarations describe controls the native inventory does not list.
		if rule, ok := modelinfo.MatchThinkingRule(model); ok && rule.Controls != nil {
			return rule.Controls.Clone()
		}
		return &modelinfo.ThinkingCapabilities{State: "supported", CanEnable: true, CanDisable: true}
	default:
		return nil
	}
}
