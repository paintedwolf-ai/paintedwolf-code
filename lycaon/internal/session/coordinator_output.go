package session

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// SanitizeToolOutputForCoordinator optionally cleans coordinator-visible tool results.
func SanitizeToolOutputForCoordinator(sess *api.Session, toolName, output string) string {
	if !isCoordinatorParentSession(sess) {
		return output
	}
	switch strings.TrimSpace(toolName) {
	case "delegate_dispatch", "task", "worker_summary":
		return guidance.StripHostBlocks(output)
	default:
		return output
	}
}

func isCoordinatorParentSession(sess *api.Session) bool {
	if sess == nil || strings.TrimSpace(sess.ParentSessionID) != "" {
		return false
	}
	agent := strings.TrimSpace(sess.AgentType)
	if agent == "" {
		agent = "coordinator"
	}
	return agent == "coordinator"
}
