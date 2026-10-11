package toolapproval

import (
	"github.com/lycaon/lycaon/internal/tools"

	"context"

	"github.com/lycaon/lycaon/internal/hitl"
)

// AIRationaleAttachRequest targets an existing tool-approval checkpoint.
type AIRationaleAttachRequest struct {
	CheckpointID string
	ToolContext  tools.ToolContext
	Tool         string
	Args         map[string]any
	Files        []string
	Explanation  *hitl.ApprovalExplanation
}

// AIRationaleAttacher adds a summary asynchronously after RequestCheckpoint.
type AIRationaleAttacher interface {
	// Enabled lets the UI reserve space for a pending rationale.
	Enabled() bool
	AttachAsync(ctx context.Context, req AIRationaleAttachRequest)
}
