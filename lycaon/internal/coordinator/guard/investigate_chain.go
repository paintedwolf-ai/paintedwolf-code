package guard

import (
	"github.com/lycaon/lycaon/pkg/api"
)

// LastInvestigateChainBoundary returns the history index after the most recent
// investigate chain break (task dispatch or worker lifecycle), or 0 at session start.
func LastInvestigateChainBoundary(history []api.Message) int {
	boundary := 0
	for i, msg := range history {
		if breaksInvestigateChain(msg) {
			boundary = i + 1
		}
	}
	return boundary
}
