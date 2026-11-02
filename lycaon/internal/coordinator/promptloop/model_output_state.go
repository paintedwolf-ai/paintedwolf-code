package promptloop

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/responserecovery"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// modelOutputState freezes a response's identity, policy and observed source.
type modelOutputState struct {
	policyCheck      tools.AgentPolicyCheck
	id               string
	iteration        int
	sourceContext    *api.SourceContext
	rejection        *guidance.Refusal
	recovery         responserecovery.State
	truncatedRetries int
	truncatedOutcome *promptresult.OutputTruncatedOutcome
}
