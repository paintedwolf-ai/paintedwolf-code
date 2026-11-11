package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/orchestration"
)

const CoordinatorTaskEnvelopeEchoCode = "COORDINATOR_TASK_ENVELOPE_ECHO"

// assistantEchoesTaskEnvelope looks only at HTML nodes of the Markdown parse, so a
// code example stays literal and prose around the envelope cannot hide it.
func assistantEchoesTaskEnvelope(content string) bool {
	doc := citationmd.Parse(content, citationmd.Options{})
	for _, zone := range doc.Zones {
		if zone.Kind != citationmd.ZoneRawHTML && zone.Kind != citationmd.ZoneHTMLBlock {
			continue
		}
		if attrs, ok := surface.TaskEnvelopeAttributes(doc.ZoneText(zone)); ok && strings.TrimSpace(attrs["job_id"]) != "" {
			return true
		}
	}
	return false
}

// IsCoordinatorProfile reports whether profileID is the coordinator agent.
func IsCoordinatorProfile(profileID string) bool {
	return strings.TrimSpace(profileID) == orchestration.ProfileCoordinator
}
