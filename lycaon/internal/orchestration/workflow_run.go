package orchestration

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// WorkflowRunLifecycle gates orchestrated runs against WorkflowRun state.
type WorkflowRunLifecycle interface {
	Start(ctx context.Context, sessionID string, req api.StartWorkflowRunRequest) (*api.WorkflowRun, error)
	Get(ctx context.Context, runID string) (*api.WorkflowRun, error)
	Cancel(ctx context.Context, runID, reason string) (*api.WorkflowRun, error)
	Fail(ctx context.Context, runID string, failure api.WorkflowFailure) (*api.WorkflowRun, error)
	MarkTopologyStageComplete(ctx context.Context, runID, stage, output, designForkCriterion string) error
	AssertRunnable(ctx context.Context, runID string) error
}
