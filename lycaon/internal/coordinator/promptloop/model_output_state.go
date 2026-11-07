package promptloop

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/responserecovery"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// modelOutputState tracks response validation and bounded recovery.
type modelOutputState struct {
	policyCheck      tools.AgentPolicyCheck
	id               string
	iteration        int
	sourceContext    *api.SourceContext
	rejection        *guidance.Refusal
	recovery         responserecovery.State
	recoveryAdmitted bool
	truncatedRetries int
	truncatedOutcome *promptresult.OutputTruncatedOutcome
}
