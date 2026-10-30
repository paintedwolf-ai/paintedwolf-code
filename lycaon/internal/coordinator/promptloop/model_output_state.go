package promptloop

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// modelOutputState keeps a response's durable identity, source provenance, and
// policy refusal together until that response is settled, and counts the
// consecutive responses whose native call the provider dropped as invalid JSON.
type modelOutputState struct {
	policyCheck           tools.AgentPolicyCheck
	id                    string
	sourceContext         *api.SourceContext
	rejection             *guidance.Refusal
	droppedToolCallStreak int
}
