package webresearch

import (
	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	directCardKind  = "direct"
	directCardLabel = "Direct search"
)

// DirectCardContent builds the direct-search card payload for settings UI.
func DirectCardContent() wire.WebResearchDirectCardContent {
	return wire.WebResearchDirectCardContent{
		ProviderID: string(wire.WebSearchProviderDirect),
		Kind:       directCardKind,
		Label:      directCardLabel,
	}
}
