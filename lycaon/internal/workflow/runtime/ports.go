package runtime

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
}
type ApprovalState interface {
	AwaitsHumanApproval(context.Context, *api.WorkflowRun, map[string]any) (bool, error)
	InheritedApprovalSnapshot(context.Context, *api.WorkflowRun) *inject.BlueprintApprovalView
}
type CoverageFacts interface {
	CoverageFacts(context.Context, *api.WorkflowRun, workflowdef.Manifest) (reviewcoverage.Facts, error)
}
type ObligationHolds interface {
	HostObligationHeld(context.Context, string) (bool, error)
}
