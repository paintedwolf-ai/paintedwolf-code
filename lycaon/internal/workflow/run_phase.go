package workflow

import (
	"context"
	"fmt"
	"strings"
)

// PhaseForRun implements delegation.WorkflowRunPhaseResolver.
func (m *RunManager) PhaseForRun(ctx context.Context, workflowRunID string) (string, error) {
	if m == nil || m.Store == nil {
		return "", fmt.Errorf("workflow store not configured")
	}
	run, err := m.Get(ctx, strings.TrimSpace(workflowRunID))
	if err != nil {
		return "", err
	}
	if run == nil {
		return "", fmt.Errorf("workflow run %q not found", workflowRunID)
	}
	return strings.TrimSpace(run.CurrentPhase), nil
}
