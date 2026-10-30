package toolexecution

import (
	"context"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// admitCredentialFiles reconciles project evidence before either entry dispatches.
// Program output has no reliable file attribution, so it uses this shared index.
func admitCredentialFiles(ctx context.Context, tool string, tc tools.ToolContext) *toolrejection.ToolReject {
	if tc.Effects.CredentialFiles == nil {
		return nil
	}
	if err := tc.Effects.CredentialFiles.EnsureProject(ctx, tc.Identity.ProjectID); err != nil {
		return &toolrejection.ToolReject{
			Code: toolrejection.OutboundSecretScreenFailedCode,
			Data: map[string]any{
				"surface":     tool,
				"fault_stage": secretmatch.FaultStageManagedSecretStore,
				"reason":      err.Error(),
			},
		}
	}
	return nil
}
