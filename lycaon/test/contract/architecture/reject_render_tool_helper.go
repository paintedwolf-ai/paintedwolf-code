package contract

import (
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
)

// renderToolFor selects a valid render target for a rejection card.
func renderToolFor(entry guidance.HintEntry, fallback string) string {
	for _, tool := range entry.Tools {
		if tool = strings.TrimSpace(tool); tool != "" {
			return tool
		}
	}
	return fallback
}
