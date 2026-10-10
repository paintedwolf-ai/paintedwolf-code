package inputs_test

import (
	"context"
	workflow "github.com/lycaon/lycaon/internal/workflow"

	"github.com/lycaon/lycaon/pkg/api"
)

func startRun(ctx context.Context, mgr *workflow.RunManager, sessionID, workflowID, version string) (*api.WorkflowRun, error) {
	return mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      workflowID,
		WorkflowVersion: version,
		Request:         "test request",
	})
}
