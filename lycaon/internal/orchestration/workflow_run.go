package orchestration

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

type WorkflowRunLifecycle struct {
	Runs     WorkflowRuns
	Starts   WorkflowStarts
	Controls WorkflowControls
	Policy   WorkflowPolicy
	Topology WorkflowTopology
}
type WorkflowRuns interface {
	Get(context.Context, string) (*api.WorkflowRun, error)
}
type WorkflowStarts interface {
	Start(context.Context, string, api.StartWorkflowRunRequest) (*api.WorkflowRun, error)
}
type WorkflowControls interface {
	Cancel(context.Context, string, string) (*api.WorkflowRun, error)
	Fail(context.Context, string, api.WorkflowFailure) (*api.WorkflowRun, error)
}
type WorkflowPolicy interface {
	AssertRunnable(context.Context, string) error
}
type WorkflowTopology interface {
	MarkTopologyStageComplete(context.Context, string, string, string, string) error
}
