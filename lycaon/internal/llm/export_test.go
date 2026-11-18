package llm

import "github.com/lycaon/lycaon/internal/llm/providerprofile"

// DiscoveryKeepSetGap returns the documented capability gap for strategy, if any.
func DiscoveryKeepSetGap(strategy providerprofile.DiscoveryStrategy) (string, bool) {
	gap, ok := discoveryKeepSet[strategy]
	return gap, ok
}
