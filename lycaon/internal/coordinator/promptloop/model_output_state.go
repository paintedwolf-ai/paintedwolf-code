package promptloop

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// modelOutputState freezes a response's identity, policy and observed source.
type modelOutputState struct {
	policyCheck           tools.AgentPolicyCheck
	id                    string
	sourceContext         *api.SourceContext
	rejection             *guidance.Refusal
	droppedToolCallStreak int
	truncatedRetries      int
	truncatedOutcome      *promptresult.OutputTruncatedOutcome
}
