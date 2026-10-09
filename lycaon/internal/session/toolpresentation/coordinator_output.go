package toolpresentation

import (
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// SanitizeForCoordinator optionally cleans coordinator-visible tool results.
func SanitizeForCoordinator(sess *api.Session, toolName, output string) string {
	if !surface.IsCoordinatorParent(sess) {
		return output
	}
	switch strings.TrimSpace(toolName) {
	case "delegate_dispatch", "task", "worker_summary":
		return guidance.StripHostBlocks(output)
	default:
		return output
	}
}
