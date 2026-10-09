package inputs

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type Sessions interface {
	Get(context.Context, string) (*api.Session, error)
	GetMessages(context.Context, string) ([]api.Message, error)
	UpdateMessage(context.Context, string, string, api.Message) (api.Message, error)
}
type Ambient interface {
	EnsureSessionWorkflow(context.Context, string) (*api.WorkflowRun, error)
}
type PhaseActivation interface {
	ActivateInitial(context.Context, *api.WorkflowRun, workflowdef.Manifest, string, bool) (*api.WorkflowRun, error)
}
type FeedbackNotifications interface {
	NotifyPending(context.Context, string, map[string]any)
}

type PhaseProgress interface {
	TryAutoAdvance(context.Context, string) (*api.WorkflowRun, error)
}
type AskApprovals interface {
	AwaitsHumanApproval(context.Context, *api.WorkflowRun, map[string]any) (bool, error)
	InheritedApprovalSnapshot(context.Context, *api.WorkflowRun) *inject.BlueprintApprovalView
}

type RequestPhases interface {
	InitializeVars(context.Context, *api.Session, *api.WorkflowRun, workflowdef.Manifest, map[string]any) (map[string]any, error)
	ActivateInitial(context.Context, *api.WorkflowRun, workflowdef.Manifest, string, bool) (*api.WorkflowRun, error)
}
type RequestApprovals interface {
	AutoApproveOnPhase(context.Context, *api.WorkflowRun, workflowdef.Manifest, workflowdef.PhaseDef, map[string]any) (map[string]any, error)
}
