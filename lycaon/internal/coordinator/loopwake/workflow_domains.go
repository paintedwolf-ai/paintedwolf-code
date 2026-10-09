package loopwake

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// WorkflowDomains binds the run state and wait policies used by coordinator loops.
type WorkflowDomains struct {
	Runs        WorkflowRuns
	Approvals   WorkflowApprovals
	Obligations WorkflowObligations
}

type WorkflowRuns interface {
	ActiveBySession(context.Context, string) (*api.WorkflowRun, error)
	GetScaffoldVars(context.Context, string) (map[string]any, error)
}

type WorkflowApprovals interface {
	HumanApprovalAwaiting(context.Context, string) (bool, error)
}

type WorkflowObligations interface {
	HostObligationHeld(context.Context, string) (bool, error)
	HostObligationHoldKinds(context.Context, string) []string
}
