package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
)

const CoordinatorTaskEnvelopeEchoCode = "COORDINATOR_TASK_ENVELOPE_ECHO"

func assistantEchoesTaskEnvelope(content string) bool {
	attrs, ok := surface.TaskEnvelopeAttributes(content)
	return ok && strings.TrimSpace(attrs["job_id"]) != ""
}

// IsCoordinatorProfile reports whether profileID is the coordinator agent.
func IsCoordinatorProfile(profileID string) bool {
	return strings.TrimSpace(profileID) == orchestration.ProfileCoordinator
}
